package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouter_HealthIsPublic(t *testing.T) {
	r := NewRouter(Deps{})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health code = %d, want 200", rec.Code)
	}
}

func TestRouter_APIRequiresKey(t *testing.T) {
	r := NewRouter(Deps{APIKeyHashHex: hashOf("k")})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ping without key = %d, want 401", rec.Code)
	}
}

func TestRouter_CORSExactMatchOnly(t *testing.T) {
	r := NewRouter(Deps{AllowedOrigins: []string{"http://localhost"}})

	// A suffix-attack origin must NOT be allowed (the original used starts_with).
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "http://localhost.evil.com")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("evil origin was allowed: %q", got)
	}

	// The exact origin is allowed.
	req2 := httptest.NewRequest(http.MethodGet, "/health", nil)
	req2.Header.Set("Origin", "http://localhost")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if got := rec2.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost" {
		t.Fatalf("exact origin not allowed: %q", got)
	}
}
