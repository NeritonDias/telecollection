package auth

// This file implements the Service contract (see service.go) over gotd's
// synchronous, callback-driven login flow (github.com/gotd/td/telegram/auth).
//
// The problem it solves: gotd's auth.Flow expects a single UserAuthenticator
// whose Phone/Code/Password methods are called *inline* while the flow runs, so
// the whole login is one blocking call. Our transport is the opposite: an HTTP
// client calls StartLogin, then SubmitCode, then (maybe) SubmitPassword in
// separate requests, and expects each to return promptly.
//
// The bridge is a goroutine plus channels. StartLogin launches the gotd flow in
// its own goroutine and blocks only until the flow asks for the code; the
// authenticator's Code/Password methods block on channels that SubmitCode /
// SubmitPassword feed. State transitions are driven by observing which callback
// the flow reached next (code requested, password requested, or flow finished).
//
// Secret material — phone number, code, 2FA password and the QR token — is never
// logged.

import (
	"context"
	"errors"
	"fmt"
	"sync"

	tgauth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/telegram/client"
)

// Ordering / validation errors. ErrPasswordNeeded is part of the public
// contract and lives in service.go.
var (
	errEmptyPhone        = errors.New("auth: phone number is required")
	errLoginInProgress   = errors.New("auth: a login is already in progress")
	errNoLoginInProgress = errors.New("auth: no login in progress")
)

// loginBackend abstracts the gotd-driven side of the login flow so the state
// machine can be unit-tested without a network or credentials. RunAuth must
// call the authenticator the way gotd's Flow does: Phone, then Code, then
// Password only if the account has 2FA. It returns when the flow finishes
// (nil on success) or fails.
type loginBackend interface {
	RunAuth(ctx context.Context, a tgauth.UserAuthenticator) error
	ExportQR(ctx context.Context) (qrURL string, err error)
}

// Flow is the stateful Service implementation. One Flow drives at most one login
// attempt at a time; its state is guarded by mu. The per-attempt goroutine and
// channels are recreated by each StartLogin and torn down on completion or
// Logout, so a fresh attempt never observes a previous attempt's channels.
type Flow struct {
	backend loginBackend

	mu    sync.Mutex
	state State

	// Per-attempt state, valid only while a login is in progress. cancel stops
	// the login goroutine; the channels bridge it to the Submit* methods.
	cancel context.CancelFunc
	auth   *flowAuthenticator
	done   chan error // buffered (cap 1); receives the flow's terminal error
}

// NewService builds a Flow backed by a real Telegram client. It returns the
// concrete type (idiomatic Go: return concrete, accept the Service interface),
// which satisfies auth.Service.
//
// NOTE: the phase-1.4 client wrapper (internal/telegram/client) currently
// exposes only Run and Dispatcher, so the real backend cannot yet execute the
// gotd login flow and StartLogin will report errBackendNotWired. See flow_test.go
// TestNewService_RealBackendReportsNotWired and the task report.
func NewService(cl *client.Client) *Flow {
	return newService(clientBackend{cl: cl})
}

// newService is the test-facing constructor: it accepts any loginBackend so the
// full state machine can be exercised with a fake, network-free backend.
func newService(b loginBackend) *Flow {
	return &Flow{
		backend: b,
		state:   StateLoggedOut,
	}
}

// StartLogin requests a login code for phone and blocks only until the flow has
// asked for that code (state -> StateWaitCode) or terminated early. The login
// goroutine it launches deliberately uses a background context, not ctx, so it
// outlives this HTTP request and remains alive for the follow-up SubmitCode /
// SubmitPassword requests.
func (s *Flow) StartLogin(ctx context.Context, phone string) error {
	if phone == "" {
		return errEmptyPhone
	}

	s.mu.Lock()
	if s.state == StateWaitCode || s.state == StateWaitPassword {
		s.mu.Unlock()
		return errLoginInProgress
	}
	// Tear down any prior finished/abandoned attempt before starting fresh.
	s.resetLocked()

	loginCtx, cancel := context.WithCancel(context.Background())
	a := newFlowAuthenticator(phone)
	done := make(chan error, 1)
	s.cancel = cancel
	s.auth = a
	s.done = done
	backend := s.backend
	s.mu.Unlock()

	go func() { done <- backend.RunAuth(loginCtx, a) }()

	select {
	case <-a.codeReq:
		// Flow asked for the code: ready for SubmitCode.
		s.setState(StateWaitCode)
		return nil
	case err := <-done:
		// Flow ended before asking for a code: either the session was already
		// authorized (err == nil) or sending the code failed (err != nil).
		return s.finish(err)
	case <-ctx.Done():
		// Caller abandoned the request: cancel the attempt and reset.
		s.mu.Lock()
		s.resetLocked()
		s.state = StateLoggedOut
		s.mu.Unlock()
		return ctx.Err()
	}
}

// SubmitCode delivers the login code to the waiting flow. It returns
// ErrPasswordNeeded (state -> StateWaitPassword) when the account has 2FA, nil
// when login completes, or the flow's error when the code is rejected.
func (s *Flow) SubmitCode(ctx context.Context, code string) error {
	s.mu.Lock()
	if s.state != StateWaitCode {
		st := s.state
		s.mu.Unlock()
		return fmt.Errorf("auth: cannot submit code in state %q: %w", st, errNoLoginInProgress)
	}
	a := s.auth
	done := s.done
	s.mu.Unlock()

	// Hand the code to the blocked authenticator.
	select {
	case a.codeCh <- code:
	case err := <-done:
		return s.finish(err)
	case <-ctx.Done():
		return ctx.Err()
	}

	// Observe what the flow does next.
	select {
	case <-a.passReq:
		s.setState(StateWaitPassword)
		return ErrPasswordNeeded
	case err := <-done:
		return s.finish(err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SubmitPassword completes 2FA by delivering the password to the waiting flow.
func (s *Flow) SubmitPassword(ctx context.Context, password string) error {
	s.mu.Lock()
	if s.state != StateWaitPassword {
		st := s.state
		s.mu.Unlock()
		return fmt.Errorf("auth: cannot submit password in state %q: %w", st, errNoLoginInProgress)
	}
	a := s.auth
	done := s.done
	s.mu.Unlock()

	select {
	case a.passCh <- password:
	case err := <-done:
		return s.finish(err)
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-done:
		return s.finish(err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StartQR begins QR login and returns the tg://login token URL to render.
func (s *Flow) StartQR(ctx context.Context) (string, error) {
	url, err := s.backend.ExportQR(ctx)
	if err != nil {
		return "", fmt.Errorf("auth: starting QR login: %w", err)
	}
	return url, nil
}

// Status reports the current login state.
func (s *Flow) Status(_ context.Context) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}

// Logout ends the current session/attempt and returns to StateLoggedOut. It
// cancels any in-flight login goroutine so no resources leak.
//
// NOTE: server-side session invalidation (auth.logOut) also needs the client
// accessor described on errBackendNotWired; until then Logout resets local state
// and stops the attempt.
func (s *Flow) Logout(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetLocked()
	s.state = StateLoggedOut
	return nil
}

// finish records a flow's terminal result: nil -> StateLoggedIn; otherwise the
// attempt is torn down, state returns to StateLoggedOut and the error is wrapped.
func (s *Flow) finish(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.resetLocked()
		s.state = StateLoggedOut
		return fmt.Errorf("auth: login failed: %w", err)
	}
	s.cancel = nil // the goroutine has returned; nothing left to cancel
	s.auth = nil
	s.done = nil
	s.state = StateLoggedIn
	return nil
}

// setState swaps the state under the lock.
func (s *Flow) setState(st State) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
}

// resetLocked cancels any running attempt and clears per-attempt state. Callers
// must hold mu. Cancelling makes a blocked authenticator return ctx.Err(), so
// the login goroutine unwinds and writes to its (buffered) done channel before
// exiting — no goroutine leak.
func (s *Flow) resetLocked() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.auth = nil
	s.done = nil
}

// flowAuthenticator implements gotd's tgauth.UserAuthenticator by blocking on
// channels instead of prompting a terminal. Code and Password each signal, once,
// that the flow reached them (by closing codeReq/passReq) and then block until
// the corresponding value arrives or ctx is cancelled.
type flowAuthenticator struct {
	phone string

	codeReq  chan struct{} // closed once, when Code is first called
	codeCh   chan string
	codeOnce sync.Once

	passReq  chan struct{} // closed once, when Password is first called
	passCh   chan string
	passOnce sync.Once
}

func newFlowAuthenticator(phone string) *flowAuthenticator {
	return &flowAuthenticator{
		phone:   phone,
		codeReq: make(chan struct{}),
		codeCh:  make(chan string),
		passReq: make(chan struct{}),
		passCh:  make(chan string),
	}
}

// Phone returns the phone number the login was started with.
func (a *flowAuthenticator) Phone(_ context.Context) (string, error) {
	return a.phone, nil
}

// Code signals that the login code was requested, then blocks until SubmitCode
// provides it (or ctx is cancelled).
func (a *flowAuthenticator) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	a.codeOnce.Do(func() { close(a.codeReq) })
	select {
	case code := <-a.codeCh:
		return code, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Password signals that 2FA was requested, then blocks until SubmitPassword
// provides the password (or ctx is cancelled). gotd calls this only after
// SignIn reports SESSION_PASSWORD_NEEDED (see tgauth.ErrPasswordAuthNeeded).
func (a *flowAuthenticator) Password(ctx context.Context) (string, error) {
	a.passOnce.Do(func() { close(a.passReq) })
	select {
	case pw := <-a.passCh:
		return pw, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// AcceptTermsOfService accepts the ToS. Only reached on the sign-up path, which
// this service does not use for existing accounts; accepting is a no-op here.
func (a *flowAuthenticator) AcceptTermsOfService(_ context.Context, _ tg.HelpTermsOfService) error {
	return nil
}

// SignUp is rejected: this service logs into existing accounts only, never
// registers new ones.
func (a *flowAuthenticator) SignUp(_ context.Context) (tgauth.UserInfo, error) {
	return tgauth.UserInfo{}, errors.New("auth: sign up for new accounts is not supported")
}

// clientBackend is the production loginBackend. It cannot yet run the gotd flow:
// the phase-1.4 client wrapper exposes only Run and Dispatcher, not Auth()/QR().
// See errBackendNotWired.
type clientBackend struct {
	cl *client.Client
}

func (b clientBackend) RunAuth(ctx context.Context, a tgauth.UserAuthenticator) error {
	if b.cl == nil {
		return errors.New("auth: nil telegram client")
	}
	// Connect, then run the gotd login flow; the authenticator (driven by our
	// state machine over channels) supplies phone/code/password on demand.
	return b.cl.Run(ctx, func(ctx context.Context) error {
		return b.cl.Auth().IfNecessary(ctx, tgauth.NewFlow(a, tgauth.SendCodeOptions{}))
	})
}

func (b clientBackend) ExportQR(ctx context.Context) (string, error) {
	if b.cl == nil {
		return "", errors.New("auth: nil telegram client")
	}
	var qrURL string
	err := b.cl.Run(ctx, func(ctx context.Context) error {
		tok, err := b.cl.QR().Export(ctx)
		if err != nil {
			return err
		}
		qrURL = tok.URL()
		return nil
	})
	return qrURL, err
}
