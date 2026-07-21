package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/telecollection/telecollection/internal/telegram/auth"
)

// fakeService is a configurable auth.Service double for testing the HTTP layer.
type fakeService struct {
	startErr    error
	codeErr     error
	passwordErr error
	qrURL       string
	qrErr       error
	state       auth.State
	statusErr   error
	logoutErr   error

	gotPhone    string
	gotCode     string
	gotPassword string
}

func (f *fakeService) StartLogin(_ context.Context, phone string) error {
	f.gotPhone = phone
	return f.startErr
}

func (f *fakeService) SubmitCode(_ context.Context, code string) error {
	f.gotCode = code
	return f.codeErr
}

func (f *fakeService) SubmitPassword(_ context.Context, password string) error {
	f.gotPassword = password
	return f.passwordErr
}

func (f *fakeService) StartQR(_ context.Context) (string, error) {
	return f.qrURL, f.qrErr
}

func (f *fakeService) Status(_ context.Context) (auth.State, error) {
	return f.state, f.statusErr
}

func (f *fakeService) Logout(_ context.Context) error {
	return f.logoutErr
}

// mountAuth builds a bare chi router with only the auth routes registered.
func mountAuth(svc auth.Service) http.Handler {
	r := chi.NewRouter()
	RegisterAuthRoutes(r, svc)
	return r
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeState(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return out.State
}

func TestAuthStart_CallsStartLoginAndReturnsWaitCode(t *testing.T) {
	f := &fakeService{}
	rec := doJSON(t, mountAuth(f), http.MethodPost, "/auth/start", `{"phone":"+5562999999999"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if f.gotPhone != "+5562999999999" {
		t.Fatalf("StartLogin got phone %q, want +5562999999999", f.gotPhone)
	}
	if s := decodeState(t, rec); s != string(auth.StateWaitCode) {
		t.Fatalf("state = %q, want %q", s, auth.StateWaitCode)
	}
}

func TestAuthCode_PasswordNeededReturnsWaitPassword(t *testing.T) {
	f := &fakeService{codeErr: auth.ErrPasswordNeeded}
	rec := doJSON(t, mountAuth(f), http.MethodPost, "/auth/code", `{"code":"12345"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if f.gotCode != "12345" {
		t.Fatalf("SubmitCode got code %q, want 12345", f.gotCode)
	}
	if s := decodeState(t, rec); s != string(auth.StateWaitPassword) {
		t.Fatalf("state = %q, want %q", s, auth.StateWaitPassword)
	}
}

func TestAuthCode_OKReturnsLoggedIn(t *testing.T) {
	f := &fakeService{}
	rec := doJSON(t, mountAuth(f), http.MethodPost, "/auth/code", `{"code":"12345"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if s := decodeState(t, rec); s != string(auth.StateLoggedIn) {
		t.Fatalf("state = %q, want %q", s, auth.StateLoggedIn)
	}
}

func TestAuthQR_ReturnsQRURL(t *testing.T) {
	const url = "tg://login?token=abc123"
	f := &fakeService{qrURL: url}
	rec := doJSON(t, mountAuth(f), http.MethodPost, "/auth/qr", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var out struct {
		QRURL string `json:"qr_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.QRURL != url {
		t.Fatalf("qr_url = %q, want %q", out.QRURL, url)
	}
}

func TestAuthStatus_ReturnsState(t *testing.T) {
	f := &fakeService{state: auth.StateWaitPassword}
	rec := doJSON(t, mountAuth(f), http.MethodGet, "/auth/status", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if s := decodeState(t, rec); s != string(auth.StateWaitPassword) {
		t.Fatalf("state = %q, want %q", s, auth.StateWaitPassword)
	}
}

func TestAuthStart_MalformedBodyReturns400(t *testing.T) {
	f := &fakeService{}
	rec := doJSON(t, mountAuth(f), http.MethodPost, "/auth/start", `{"phone":`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (body %q)", rec.Code, rec.Body.String())
	}
	if f.gotPhone != "" {
		t.Fatalf("StartLogin should not be called on malformed body, got phone %q", f.gotPhone)
	}
}

func TestAuthPassword_ReturnsLoggedIn(t *testing.T) {
	f := &fakeService{}
	rec := doJSON(t, mountAuth(f), http.MethodPost, "/auth/password", `{"password":"hunter2"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if f.gotPassword != "hunter2" {
		t.Fatalf("SubmitPassword got %q, want hunter2", f.gotPassword)
	}
	if s := decodeState(t, rec); s != string(auth.StateLoggedIn) {
		t.Fatalf("state = %q, want %q", s, auth.StateLoggedIn)
	}
}

func TestAuthLogout_Returns204(t *testing.T) {
	f := &fakeService{}
	rec := doJSON(t, mountAuth(f), http.MethodPost, "/auth/logout", "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestAuthStart_BusinessErrorReturns500(t *testing.T) {
	f := &fakeService{startErr: context.DeadlineExceeded}
	rec := doJSON(t, mountAuth(f), http.MethodPost, "/auth/start", `{"phone":"+5562999999999"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500 (body %q)", rec.Code, rec.Body.String())
	}
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	if out.Error.Code == "" {
		t.Fatalf("error.code empty in body %q", rec.Body.String())
	}
}

// TestAuthRoutes_WiredIntoRouter verifies the routes are reachable through the
// full router behind the API key middleware.
func TestAuthRoutes_WiredIntoRouter(t *testing.T) {
	f := &fakeService{}
	r := NewRouter(Deps{APIKeyHashHex: hashOf("k"), Auth: f})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/start", strings.NewReader(`{"phone":"+55"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "k")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("wired auth/start = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if f.gotPhone != "+55" {
		t.Fatalf("wired StartLogin got phone %q, want +55", f.gotPhone)
	}
}
