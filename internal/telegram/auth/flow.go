package auth

// This file implements the Service contract (see service.go) over gotd's
// synchronous, callback-driven login flow (github.com/gotd/td/telegram/auth and
// .../auth/qrlogin).
//
// The problem it solves: gotd's auth.Flow expects a single UserAuthenticator
// whose Phone/Code/Password methods are called *inline* while the flow runs, so
// the whole login is one blocking call; qrlogin.QR.Auth is likewise a single
// blocking call that shows a token and then waits for the phone to scan it. Our
// transport is the opposite: an HTTP client calls StartLogin, then SubmitCode,
// then (maybe) SubmitPassword in separate requests, and expects each to return
// promptly.
//
// The bridge is a goroutine plus channels. StartLogin / StartQR each launch the
// gotd flow in its own goroutine and block only until there is something to
// return (the code was requested, or the QR URL was produced). The
// authenticator's Code/Password methods block on channels that SubmitCode /
// SubmitPassword feed.
//
// State ownership (the invariant that keeps the machine race-free): the login
// goroutine is the SOLE writer of the login state. The authenticator sets
// StateWaitCode when it enters Code and StateWaitPassword when it enters
// Password; the goroutine sets StateLoggedIn / StateLoggedOut when the flow
// finishes. SubmitCode / SubmitPassword only DELIVER a value to the waiting
// flow and then observe the outcome — they never derive state themselves, so a
// request context cancelled mid-flight cannot desynchronise the machine from the
// real flow. Each attempt carries a generation number; a superseded goroutine's
// late state writes are ignored, so preemption and Logout never corrupt a newer
// attempt.
//
// Secret material — phone number, code, 2FA password and the QR token — is never
// logged.

import (
	"context"
	"errors"
	"fmt"
	"sync"

	tgauth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
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
// machine can be unit-tested without a network or credentials.
//
// RunAuth must call the authenticator the way gotd's Flow does: Phone, then
// Code, then Password only if the account has 2FA. RunQR must call onToken with
// the tg://login URL as soon as it is available and then block until the phone
// scans it. Both return when the flow finishes (nil on success) or fails, and
// both must honour ctx cancellation so a superseded attempt can be torn down.
type loginBackend interface {
	RunAuth(ctx context.Context, a tgauth.UserAuthenticator) error
	RunQR(ctx context.Context, onToken func(url string)) error
}

// Flow is the stateful Service implementation. One Flow drives at most one login
// attempt at a time; its state is guarded by mu. Each attempt owns a fresh
// generation, cancel func, authenticator and done channel, recreated on every
// StartLogin / StartQR and torn down on completion, preemption or Logout, so a
// fresh attempt never observes a previous attempt's channels or state writes.
type Flow struct {
	backend loginBackend

	mu    sync.Mutex
	state State

	// inProgress marks, under mu, that an attempt goroutine is live. It closes
	// the StartLogin-vs-StartLogin race: the intent is recorded before mu is
	// released, so a concurrent StartLogin cannot slip past the guard.
	inProgress bool

	// gen identifies the current attempt. resetLocked increments it, disowning
	// any in-flight goroutine so its late state writes (transition/finishAttempt)
	// become no-ops.
	gen uint64

	// Per-attempt state, valid only while a login is in progress. cancel stops
	// the login goroutine; done (buffered, cap 1) receives the flow's terminal
	// error; auth bridges the goroutine to the Submit* methods.
	cancel context.CancelFunc
	auth   *flowAuthenticator
	done   chan error
}

// NewService builds a Flow backed by a real Telegram client. It returns the
// concrete type (idiomatic Go: return concrete, accept the Service interface),
// which satisfies auth.Service.
//
// The backend drives gotd over the phase-1.4 client wrapper: RunAuth uses
// client.Run + client.Auth(), and RunQR uses client.Run + client.QR() +
// client.Dispatcher(). Both paths therefore need real credentials and a live
// connection; they are exercised by the end-to-end validation, while the state
// machine itself is unit-tested with a network-free fake backend.
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
// asked for that code (the authenticator has set StateWaitCode) or the flow
// terminated early. The login goroutine it launches deliberately uses a
// background context, not ctx, so it outlives this HTTP request and remains
// alive for the follow-up SubmitCode / SubmitPassword requests.
//
// A second concurrent StartLogin is rejected with errLoginInProgress: the intent
// is recorded under mu before it is released, so exactly one caller wins.
func (s *Flow) StartLogin(ctx context.Context, phone string) error {
	if phone == "" {
		return errEmptyPhone
	}

	s.mu.Lock()
	if s.inProgress {
		s.mu.Unlock()
		return errLoginInProgress
	}
	s.resetLocked() // clear any prior finished attempt; increments gen
	gen := s.gen
	s.inProgress = true

	loginCtx, cancel := context.WithCancel(context.Background())
	a := newFlowAuthenticator(
		phone,
		func() { s.transition(gen, StateWaitCode) },
		func() { s.transition(gen, StateWaitPassword) },
	)
	done := make(chan error, 1)
	s.cancel = cancel
	s.auth = a
	s.done = done
	backend := s.backend
	s.mu.Unlock()

	go func() {
		err := backend.RunAuth(loginCtx, a)
		s.finishAttempt(gen, err)
		done <- err
	}()

	select {
	case <-a.codeReq:
		// Flow asked for the code: the authenticator already set StateWaitCode.
		return nil
	case err := <-done:
		// Flow ended before asking for a code: either the session was already
		// authorized (err == nil) or sending the code failed (err != nil). The
		// goroutine has already recorded the terminal state.
		return attemptError(err)
	case <-ctx.Done():
		// Caller abandoned the request: cancel the attempt and reset.
		s.abandon(gen)
		return ctx.Err()
	}
}

// SubmitCode delivers the login code to the waiting flow. It returns
// ErrPasswordNeeded when the account has 2FA (the authenticator has set
// StateWaitPassword), nil when login completes, or the flow's error when the
// code is rejected. It only delivers and observes; the goroutine owns the state.
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
		return attemptError(err)
	case <-ctx.Done():
		return ctx.Err()
	}

	// Observe what the flow does next. State is set by the flow goroutine
	// (Password -> StateWaitPassword, or the goroutine's terminal write), so a
	// ctx cancellation here returns promptly without corrupting the machine.
	select {
	case <-a.passReq:
		return ErrPasswordNeeded
	case err := <-done:
		return attemptError(err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SubmitPassword completes 2FA by delivering the password to the waiting flow.
// Like SubmitCode it only delivers and observes; the goroutine owns the state.
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
		return attemptError(err)
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-done:
		return attemptError(err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StartQR begins QR login and returns the tg://login token URL to render. Like
// StartLogin it launches a single goroutine that runs the whole blocking gotd
// flow (qrlogin.QR.Auth, which shows the token then awaits the scan) and owns
// the state; when the scan completes the goroutine transitions to StateLoggedIn.
//
// QR is the explicit fallback for a code login: it preempts any pending attempt
// (resetLocked cancels the code goroutine). To respect gotd's rule that Run is
// not reentrant, StartQR waits for a preempted goroutine to fully unwind before
// launching its own Run.
func (s *Flow) StartQR(ctx context.Context) (string, error) {
	s.mu.Lock()
	prevDone := s.done
	prevInProgress := s.inProgress
	s.resetLocked() // cancel any pending attempt; increments gen
	gen := s.gen
	s.inProgress = true
	// A pending QR login expects no code/password: leave StateLoggedOut until the
	// scan completes. This also clears any StateWaitCode left by a preempted code
	// login, so a stale SubmitCode is rejected instead of hitting a nil channel.
	s.state = StateLoggedOut

	qrCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	urlCh := make(chan string, 1)
	s.cancel = cancel
	s.done = done
	backend := s.backend
	s.mu.Unlock()

	// Wait for a preempted attempt's goroutine to write its terminal error and
	// exit before starting a new Run; gotd does not support reentrant Run.
	if prevInProgress && prevDone != nil {
		select {
		case <-prevDone:
		case <-ctx.Done():
			s.abandon(gen)
			return "", ctx.Err()
		}
	}

	go func() {
		err := backend.RunQR(qrCtx, func(u string) {
			// Publish the first token URL; later refreshes (on QR expiry) are
			// ignored because the buffer already holds a URL to render.
			select {
			case urlCh <- u:
			default:
			}
		})
		s.finishAttempt(gen, err)
		done <- err
	}()

	select {
	case u := <-urlCh:
		return u, nil
	case err := <-done:
		// Flow failed before producing a token URL.
		if err != nil {
			return "", fmt.Errorf("auth: starting QR login: %w", err)
		}
		return "", nil
	case <-ctx.Done():
		s.abandon(gen)
		return "", ctx.Err()
	}
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
// Server-side session invalidation (auth.logOut over the wire) is out of scope
// for this state machine; Logout resets local state and stops the attempt.
func (s *Flow) Logout(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetLocked()
	s.state = StateLoggedOut
	return nil
}

// finishAttempt records a flow's terminal result under mu: nil -> StateLoggedIn;
// otherwise StateLoggedOut. It is a no-op if the attempt was already superseded
// (gen mismatch), so a preempted or logged-out goroutine cannot clobber a newer
// attempt. On success it cancels the (now idle) attempt context to release it.
func (s *Flow) finishAttempt(gen uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		return // superseded; a newer attempt (or Logout) owns the state
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.auth = nil
	s.done = nil
	s.inProgress = false
	if err != nil {
		s.state = StateLoggedOut
	} else {
		s.state = StateLoggedIn
	}
}

// transition sets st under mu, but only while gen is still the current attempt.
// The authenticator calls it to publish StateWaitCode / StateWaitPassword.
func (s *Flow) transition(gen uint64, st State) {
	s.mu.Lock()
	if s.gen == gen {
		s.state = st
	}
	s.mu.Unlock()
}

// abandon tears the current attempt down and returns to StateLoggedOut, but only
// if gen is still current. Used when a caller's request context is cancelled
// while StartLogin / StartQR is still waiting.
func (s *Flow) abandon(gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		return
	}
	s.resetLocked()
	s.state = StateLoggedOut
}

// resetLocked cancels any running attempt and clears per-attempt state, then
// bumps gen so the disowned goroutine's late writes are ignored. Callers must
// hold mu. Cancelling makes a blocked authenticator / QR flow return ctx.Err(),
// so the login goroutine unwinds and writes to its (buffered) done channel
// before exiting — no goroutine leak.
func (s *Flow) resetLocked() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.auth = nil
	s.done = nil
	s.inProgress = false
	s.gen++
}

// attemptError formats a flow's terminal error for a Submit*/Start* return value.
// State has already been recorded by the login goroutine.
func attemptError(err error) error {
	if err != nil {
		return fmt.Errorf("auth: login failed: %w", err)
	}
	return nil
}

// flowAuthenticator implements gotd's tgauth.UserAuthenticator by blocking on
// channels instead of prompting a terminal. Code and Password each publish the
// corresponding state (onCode / onPass) and signal, once, that the flow reached
// them (by closing codeReq/passReq), then block until the value arrives or ctx
// is cancelled. onCode / onPass are the ONLY writers of StateWaitCode /
// StateWaitPassword, keeping the login goroutine the sole owner of state.
type flowAuthenticator struct {
	phone string

	onCode func() // publishes StateWaitCode; called once, before codeReq closes
	onPass func() // publishes StateWaitPassword; called once, before passReq closes

	codeReq  chan struct{} // closed once, when Code is first called
	codeCh   chan string
	codeOnce sync.Once

	passReq  chan struct{} // closed once, when Password is first called
	passCh   chan string
	passOnce sync.Once
}

func newFlowAuthenticator(phone string, onCode, onPass func()) *flowAuthenticator {
	return &flowAuthenticator{
		phone:   phone,
		onCode:  onCode,
		onPass:  onPass,
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

// Code publishes StateWaitCode, signals that the login code was requested, then
// blocks until SubmitCode provides it (or ctx is cancelled).
func (a *flowAuthenticator) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	a.codeOnce.Do(func() {
		a.onCode()
		close(a.codeReq)
	})
	select {
	case code := <-a.codeCh:
		return code, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Password publishes StateWaitPassword, signals that 2FA was requested, then
// blocks until SubmitPassword provides the password (or ctx is cancelled). gotd
// calls this only after SignIn reports SESSION_PASSWORD_NEEDED (see
// tgauth.ErrPasswordAuthNeeded).
func (a *flowAuthenticator) Password(ctx context.Context) (string, error) {
	a.passOnce.Do(func() {
		a.onPass()
		close(a.passReq)
	})
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

// clientBackend is the production loginBackend. It drives gotd over the phase-1.4
// client wrapper, which exposes Run, Auth(), QR() and Dispatcher() — everything
// the code and QR flows need.
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

func (b clientBackend) RunQR(ctx context.Context, onToken func(url string)) error {
	if b.cl == nil {
		return errors.New("auth: nil telegram client")
	}
	// Connect, then run the gotd QR flow. OnLoginToken registers a handler on
	// the shared dispatcher and returns the signal channel QR.Auth waits on; the
	// show callback publishes the token URL and QR.Auth blocks until the scan.
	return b.cl.Run(ctx, func(ctx context.Context) error {
		loggedIn := qrlogin.OnLoginToken(b.cl.Dispatcher())
		qr := b.cl.QR()
		_, err := qr.Auth(ctx, loggedIn, func(_ context.Context, token qrlogin.Token) error {
			onToken(token.URL())
			return nil
		})
		return err
	})
}
