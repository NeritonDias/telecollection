package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func hashOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func TestRequireAPIKey(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := RequireAPIKey(hashOf("secret"))(ok)

	cases := []struct {
		name string
		key  string
		set  bool
		want int
	}{
		{"missing key", "", false, http.StatusUnauthorized},
		{"wrong key", "nope", true, http.StatusUnauthorized},
		{"correct key", "secret", true, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if c.set {
				req.Header.Set("X-API-Key", c.key)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("code = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestRequireAPIKey_NoHashFailsClosed(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := RequireAPIKey("")(ok)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-API-Key", "anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unconfigured API must fail closed: code = %d, want 401", rec.Code)
	}
}
