// Package auth defines the Telegram login flow contract and (from phase 1.5) its
// implementation over gotd. The HTTP layer (phase 1.6) depends only on Service,
// so endpoints and the real flow can be built in parallel.
package auth

import (
	"context"
	"errors"
)

// State reflects where the login flow currently is.
type State string

// Login flow states.
const (
	StateLoggedOut    State = "logged_out"
	StateWaitCode     State = "wait_code"
	StateWaitPassword State = "wait_password"
	StateLoggedIn     State = "logged_in"
)

// ErrPasswordNeeded is returned by SubmitCode when the account has 2FA enabled.
var ErrPasswordNeeded = errors.New("auth: 2fa password required")

// Service drives the Telegram login flow. The real implementation arrives in
// phase 1.5; the HTTP layer (1.6) is written against this interface.
type Service interface {
	// StartLogin requests an SMS/app code for the given phone number.
	StartLogin(ctx context.Context, phone string) error
	// SubmitCode submits the received code. Returns ErrPasswordNeeded if 2FA is on.
	SubmitCode(ctx context.Context, code string) error
	// SubmitPassword completes 2FA (SRP).
	SubmitPassword(ctx context.Context, password string) error
	// StartQR begins QR login and returns the tg://login URL to render.
	StartQR(ctx context.Context) (qrURL string, err error)
	// Status reports the current login state.
	Status(ctx context.Context) (State, error)
	// Logout ends the session.
	Logout(ctx context.Context) error
}
