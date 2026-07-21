package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	tgauth "github.com/gotd/td/telegram/auth"

	"github.com/telecollection/telecollection/internal/telegram/client"
)

// --- compile-time contract assertions -------------------------------------

// *Flow must satisfy the auth.Service contract defined in service.go.
var _ Service = (*Flow)(nil)

// *flowAuthenticator must satisfy gotd's UserAuthenticator so it can drive
// tgauth.NewFlow.
var _ tgauth.UserAuthenticator = (*flowAuthenticator)(nil)

// --- fake backend ----------------------------------------------------------

// fakeBackend stands in for the real gotd login backend. It drives the
// UserAuthenticator exactly the way gotd's Flow does (Phone, then Code, then
// Password only if 2FA is required), so the real channel wiring and state
// machine are exercised without any network I/O or credentials.
type fakeBackend struct {
	need2FA  bool
	finalErr error // returned after the flow "completes"

	qrURL   string
	qrErr   error
	authErr error // returned immediately, before requesting a code
}

func (b *fakeBackend) RunAuth(ctx context.Context, a tgauth.UserAuthenticator) error {
	if b.authErr != nil {
		return b.authErr
	}
	if _, err := a.Phone(ctx); err != nil {
		return err
	}
	if _, err := a.Code(ctx, nil); err != nil {
		return err
	}
	if b.need2FA {
		if _, err := a.Password(ctx); err != nil {
			return err
		}
	}
	return b.finalErr
}

func (b *fakeBackend) ExportQR(_ context.Context) (string, error) {
	return b.qrURL, b.qrErr
}

// waitState polls until the service reaches want or the deadline elapses.
func waitState(t *testing.T, s *Flow, want State) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := s.Status(context.Background())
		if err != nil {
			t.Fatalf("Status returned error: %v", err)
		}
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	got, _ := s.Status(context.Background())
	t.Fatalf("state never reached %q; stuck at %q", want, got)
}

// --- state / validation ----------------------------------------------------

func TestStatus_InitialLoggedOut(t *testing.T) {
	s := newService(&fakeBackend{})
	got, err := s.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if got != StateLoggedOut {
		t.Fatalf("initial state = %q, want %q", got, StateLoggedOut)
	}
}

func TestStartLogin_EmptyPhone(t *testing.T) {
	s := newService(&fakeBackend{})
	if err := s.StartLogin(context.Background(), ""); err == nil {
		t.Fatal("StartLogin with empty phone: expected error, got nil")
	}
	got, _ := s.Status(context.Background())
	if got != StateLoggedOut {
		t.Fatalf("state after failed StartLogin = %q, want %q", got, StateLoggedOut)
	}
}

func TestSubmitCode_NoLoginInProgress(t *testing.T) {
	s := newService(&fakeBackend{})
	// Must not panic or deadlock; must return a clear error quickly.
	done := make(chan error, 1)
	go func() { done <- s.SubmitCode(context.Background(), "12345") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("SubmitCode out of order: expected error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SubmitCode out of order deadlocked")
	}
}

func TestSubmitPassword_NoLoginInProgress(t *testing.T) {
	s := newService(&fakeBackend{})
	done := make(chan error, 1)
	go func() { done <- s.SubmitPassword(context.Background(), "secret") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("SubmitPassword out of order: expected error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SubmitPassword out of order deadlocked")
	}
}

// --- happy path (no 2FA) ---------------------------------------------------

func TestLogin_CodeOnly(t *testing.T) {
	s := newService(&fakeBackend{need2FA: false})

	if err := s.StartLogin(context.Background(), "+15551234567"); err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	if got, _ := s.Status(context.Background()); got != StateWaitCode {
		t.Fatalf("after StartLogin state = %q, want %q", got, StateWaitCode)
	}

	if err := s.SubmitCode(context.Background(), "12345"); err != nil {
		t.Fatalf("SubmitCode: %v", err)
	}
	if got, _ := s.Status(context.Background()); got != StateLoggedIn {
		t.Fatalf("after SubmitCode state = %q, want %q", got, StateLoggedIn)
	}
}

// --- 2FA path --------------------------------------------------------------

func TestLogin_With2FA(t *testing.T) {
	s := newService(&fakeBackend{need2FA: true})

	if err := s.StartLogin(context.Background(), "+15551234567"); err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	waitState(t, s, StateWaitCode)

	err := s.SubmitCode(context.Background(), "12345")
	if !errors.Is(err, ErrPasswordNeeded) {
		t.Fatalf("SubmitCode with 2FA: got err %v, want ErrPasswordNeeded", err)
	}
	if got, _ := s.Status(context.Background()); got != StateWaitPassword {
		t.Fatalf("after 2FA SubmitCode state = %q, want %q", got, StateWaitPassword)
	}

	if err := s.SubmitPassword(context.Background(), "hunter2"); err != nil {
		t.Fatalf("SubmitPassword: %v", err)
	}
	if got, _ := s.Status(context.Background()); got != StateLoggedIn {
		t.Fatalf("after SubmitPassword state = %q, want %q", got, StateLoggedIn)
	}
}

// --- error path: wrong / rejected code ------------------------------------

func TestLogin_CodeRejected(t *testing.T) {
	s := newService(&fakeBackend{finalErr: errors.New("PHONE_CODE_INVALID")})

	if err := s.StartLogin(context.Background(), "+15551234567"); err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	waitState(t, s, StateWaitCode)

	if err := s.SubmitCode(context.Background(), "00000"); err == nil {
		t.Fatal("SubmitCode with rejected code: expected error, got nil")
	}
	if got, _ := s.Status(context.Background()); got != StateLoggedOut {
		t.Fatalf("after rejected code state = %q, want %q", got, StateLoggedOut)
	}
}

// --- already-authorized shortcut ------------------------------------------

func TestStartLogin_AlreadyAuthorized(t *testing.T) {
	// Backend returns nil before requesting a code: gotd's IfNecessary does this
	// when the stored session is already authorized.
	s := newService(&fakeBackend{authErr: nil, qrURL: ""})
	s.backend = &alreadyAuthorizedBackend{}

	if err := s.StartLogin(context.Background(), "+15551234567"); err != nil {
		t.Fatalf("StartLogin (already authorized): %v", err)
	}
	if got, _ := s.Status(context.Background()); got != StateLoggedIn {
		t.Fatalf("state = %q, want %q", got, StateLoggedIn)
	}
}

// alreadyAuthorizedBackend never calls the authenticator: the session was
// already valid, so IfNecessary returns nil immediately.
type alreadyAuthorizedBackend struct{}

func (alreadyAuthorizedBackend) RunAuth(context.Context, tgauth.UserAuthenticator) error {
	return nil
}
func (alreadyAuthorizedBackend) ExportQR(context.Context) (string, error) { return "", nil }

// --- double start ----------------------------------------------------------

func TestStartLogin_AlreadyInProgress(t *testing.T) {
	s := newService(&fakeBackend{need2FA: true})

	if err := s.StartLogin(context.Background(), "+15551234567"); err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	waitState(t, s, StateWaitCode)

	if err := s.StartLogin(context.Background(), "+15559999999"); err == nil {
		t.Fatal("second StartLogin while in progress: expected error, got nil")
	}
}

// --- authenticator behaviour ----------------------------------------------

func TestFlowAuthenticator_Phone(t *testing.T) {
	a := newFlowAuthenticator("+15551234567")
	phone, err := a.Phone(context.Background())
	if err != nil {
		t.Fatalf("Phone: %v", err)
	}
	if phone != "+15551234567" {
		t.Fatalf("Phone = %q, want +15551234567", phone)
	}
}

func TestFlowAuthenticator_SignUpRejected(t *testing.T) {
	a := newFlowAuthenticator("+15551234567")
	if _, err := a.SignUp(context.Background()); err == nil {
		t.Fatal("SignUp: expected error (sign up unsupported), got nil")
	}
}

func TestFlowAuthenticator_CodeHonoursContext(t *testing.T) {
	a := newFlowAuthenticator("+15551234567")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Code(ctx, nil); err == nil {
		t.Fatal("Code with cancelled ctx: expected error, got nil")
	}
}

// --- QR --------------------------------------------------------------------

func TestStartQR_ReturnsURL(t *testing.T) {
	const want = "tg://login?token=abcdef"
	s := newService(&fakeBackend{qrURL: want})
	got, err := s.StartQR(context.Background())
	if err != nil {
		t.Fatalf("StartQR: %v", err)
	}
	if got != want {
		t.Fatalf("StartQR URL = %q, want %q", got, want)
	}
}

func TestStartQR_BackendError(t *testing.T) {
	s := newService(&fakeBackend{qrErr: errors.New("no network")})
	if _, err := s.StartQR(context.Background()); err == nil {
		t.Fatal("StartQR with backend error: expected error, got nil")
	}
}

// --- logout ----------------------------------------------------------------

func TestLogout_ResetsState(t *testing.T) {
	s := newService(&fakeBackend{need2FA: true})
	if err := s.StartLogin(context.Background(), "+15551234567"); err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	waitState(t, s, StateWaitCode)

	if err := s.Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if got, _ := s.Status(context.Background()); got != StateLoggedOut {
		t.Fatalf("after Logout state = %q, want %q", got, StateLoggedOut)
	}
}

// --- production wiring (documents the 1.4 integration gap) ------------------

// The real client backend built by NewService is constructed correctly and
// starts in the logged-out state. Its network-driven StartLogin/StartQR paths
// need real credentials and a live connection, so they are covered by the
// end-to-end validation rather than here.
func TestNewService_RealBackendConstructs(t *testing.T) {
	cl, err := client.New(client.Config{
		APIID:       123,
		APIHash:     "abc",
		SessionPath: filepath.Join(t.TempDir(), "session.bin"),
		Key:         make([]byte, 32),
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	s := NewService(cl)
	if got, _ := s.Status(context.Background()); got != StateLoggedOut {
		t.Fatalf("initial state = %q, want %q", got, StateLoggedOut)
	}
}
