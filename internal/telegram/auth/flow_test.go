package auth

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
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
// Password only if 2FA is required), and mirrors qrlogin.QR.Auth for RunQR
// (publish the token URL, then block until a scan is signalled), so the real
// channel wiring and state machine are exercised without any network I/O or
// credentials.
type fakeBackend struct {
	need2FA  bool
	finalErr error // returned after the flow "completes"

	qrURL     string
	qrErr     error         // returned immediately from RunQR, before any token URL
	qrScan    chan struct{} // when closed, RunQR "completes" (the phone scanned)
	qrScanErr error         // terminal error RunQR returns once qrScan fires
	authErr   error         // returned immediately, before requesting a code
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

func (b *fakeBackend) RunQR(ctx context.Context, onToken func(url string)) error {
	if b.qrErr != nil {
		return b.qrErr
	}
	onToken(b.qrURL)
	// Mirror qrlogin.QR.Auth: show the token, then await the scan. A nil qrScan
	// blocks forever, so the flow only ends via ctx cancellation (preempt /
	// Logout) — this keeps StartQR's URL return deterministic (done never races
	// with urlCh) and lets tests drive completion explicitly.
	select {
	case <-b.qrScan:
		return b.qrScanErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// gatedBackend is a code-login backend whose progress past the code step is
// controlled by the test, so a request context can be cancelled at a precise
// moment mid-flight.
type gatedBackend struct {
	need2FA  bool
	codeSeen chan struct{} // signalled once, after Code delivers its value
	gate     chan struct{} // the flow blocks here until the test releases it
	finalErr error
}

func (b *gatedBackend) RunAuth(ctx context.Context, a tgauth.UserAuthenticator) error {
	if _, err := a.Phone(ctx); err != nil {
		return err
	}
	if _, err := a.Code(ctx, nil); err != nil {
		return err
	}
	if b.codeSeen != nil {
		close(b.codeSeen)
	}
	if b.gate != nil {
		select {
		case <-b.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if b.need2FA {
		if _, err := a.Password(ctx); err != nil {
			return err
		}
	}
	return b.finalErr
}

func (b *gatedBackend) RunQR(ctx context.Context, _ func(url string)) error {
	<-ctx.Done()
	return ctx.Err()
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

// --- happy path (no 2FA): state is driven by the flow goroutine ------------

func TestLogin_CodeOnly(t *testing.T) {
	s := newService(&fakeBackend{need2FA: false})

	if err := s.StartLogin(context.Background(), "+15551234567"); err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	// StateWaitCode was set by the authenticator's Code method (the goroutine),
	// not by StartLogin itself.
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

// --- 2FA path: state is driven by the flow goroutine -----------------------

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
	// StateWaitPassword was set by the authenticator's Password method.
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
	s := newService(&alreadyAuthorizedBackend{})

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
func (alreadyAuthorizedBackend) RunQR(context.Context, func(string)) error { return nil }

// --- double start: concurrent StartLogin -----------------------------------

// Two concurrent StartLogin calls must resolve to exactly one success and one
// "in progress" rejection — never two winners, never a corrupted machine. Run
// under -count=N to shake out the StartLogin-vs-StartLogin race.
func TestStartLogin_ConcurrentExactlyOneWins(t *testing.T) {
	s := newService(&fakeBackend{need2FA: true})

	const n = 2
	var wg sync.WaitGroup
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- s.StartLogin(context.Background(), "+15551234567")
		}()
	}
	close(start) // release both as simultaneously as possible
	wg.Wait()
	close(errs)

	var wins, inProgress int
	for err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, errLoginInProgress):
			inProgress++
		default:
			t.Fatalf("unexpected StartLogin error: %v", err)
		}
	}
	if wins != 1 || inProgress != 1 {
		t.Fatalf("concurrent StartLogin: wins=%d inProgress=%d, want 1 and 1", wins, inProgress)
	}
	// The winner reached StateWaitCode; the machine is coherent.
	waitState(t, s, StateWaitCode)
	_ = s.Logout(context.Background())
}

func TestStartLogin_AlreadyInProgress(t *testing.T) {
	s := newService(&fakeBackend{need2FA: true})

	if err := s.StartLogin(context.Background(), "+15551234567"); err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	waitState(t, s, StateWaitCode)

	if err := s.StartLogin(context.Background(), "+15559999999"); err == nil {
		t.Fatal("second StartLogin while in progress: expected error, got nil")
	}
	_ = s.Logout(context.Background())
}

// --- ctx cancelled mid-flight must not desynchronise the machine -----------

// If a request context is cancelled after the code was delivered but before the
// flow's next step is observed, SubmitCode must return promptly AND leave the
// machine recoverable: the flow goroutine still owns the state, so a subsequent
// SubmitPassword completes the login rather than deadlocking.
func TestSubmitCode_CtxCancelMidFlightRecovers(t *testing.T) {
	b := &gatedBackend{
		need2FA:  true,
		codeSeen: make(chan struct{}),
		gate:     make(chan struct{}),
	}
	s := newService(b)

	if err := s.StartLogin(context.Background(), "+15551234567"); err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	waitState(t, s, StateWaitCode)

	ctx, cancel := context.WithCancel(context.Background())
	subErr := make(chan error, 1)
	go func() { subErr <- s.SubmitCode(ctx, "12345") }()

	<-b.codeSeen // the flow received the code; it is now blocked on the gate
	cancel()     // caller's request context dies mid-flight

	select {
	case err := <-subErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SubmitCode after ctx cancel: got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SubmitCode did not return after its ctx was cancelled")
	}

	// Release the flow; the goroutine (sole state owner) advances to 2FA.
	close(b.gate)
	waitState(t, s, StateWaitPassword)

	// A subsequent SubmitPassword must not deadlock and must finish the login.
	if err := s.SubmitPassword(context.Background(), "hunter2"); err != nil {
		t.Fatalf("SubmitPassword after recovery: %v", err)
	}
	waitState(t, s, StateLoggedIn)
}

// --- authenticator behaviour ----------------------------------------------

func TestFlowAuthenticator_Phone(t *testing.T) {
	a := newFlowAuthenticator("+15551234567", func() {}, func() {})
	phone, err := a.Phone(context.Background())
	if err != nil {
		t.Fatalf("Phone: %v", err)
	}
	if phone != "+15551234567" {
		t.Fatalf("Phone = %q, want +15551234567", phone)
	}
}

func TestFlowAuthenticator_SignUpRejected(t *testing.T) {
	a := newFlowAuthenticator("+15551234567", func() {}, func() {})
	if _, err := a.SignUp(context.Background()); err == nil {
		t.Fatal("SignUp: expected error (sign up unsupported), got nil")
	}
}

func TestFlowAuthenticator_CodeHonoursContext(t *testing.T) {
	a := newFlowAuthenticator("+15551234567", func() {}, func() {})
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
	// The QR goroutine is still awaiting a scan; Logout tears it down cleanly.
	_ = s.Logout(context.Background())
}

func TestStartQR_BackendError(t *testing.T) {
	s := newService(&fakeBackend{qrErr: errors.New("no network")})
	if _, err := s.StartQR(context.Background()); err == nil {
		t.Fatal("StartQR with backend error: expected error, got nil")
	}
	if got, _ := s.Status(context.Background()); got != StateLoggedOut {
		t.Fatalf("after QR error state = %q, want %q", got, StateLoggedOut)
	}
}

// StartQR completes: once the phone scans, the flow goroutine transitions to
// StateLoggedIn on its own.
func TestStartQR_CompletesToLoggedIn(t *testing.T) {
	scan := make(chan struct{})
	s := newService(&fakeBackend{qrURL: "tg://login?token=abc", qrScan: scan})

	if _, err := s.StartQR(context.Background()); err != nil {
		t.Fatalf("StartQR: %v", err)
	}
	if got, _ := s.Status(context.Background()); got == StateLoggedIn {
		t.Fatal("logged in before the scan was signalled")
	}

	close(scan) // the phone scanned the code
	waitState(t, s, StateLoggedIn)
}

// StartQR during a pending code login preempts it (fallback code -> QR) without
// a reentrant Run, then completes to StateLoggedIn on scan.
func TestStartQR_PreemptsPendingCodeLogin(t *testing.T) {
	scan := make(chan struct{})
	s := newService(&fakeBackend{need2FA: true, qrURL: "tg://login?token=xyz", qrScan: scan})

	if err := s.StartLogin(context.Background(), "+15551234567"); err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	waitState(t, s, StateWaitCode)

	url, err := s.StartQR(context.Background())
	if err != nil {
		t.Fatalf("StartQR (preempting code login): %v", err)
	}
	if url != "tg://login?token=xyz" {
		t.Fatalf("StartQR URL = %q, want tg://login?token=xyz", url)
	}

	// The preempted code login must no longer be accepting a code.
	if err := s.SubmitCode(context.Background(), "12345"); err == nil {
		t.Fatal("SubmitCode after QR preemption: expected error, got nil")
	}

	close(scan)
	waitState(t, s, StateLoggedIn)
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

// --- production wiring (documents the 1.4 integration) ----------------------

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
