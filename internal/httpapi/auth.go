package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
)

// RequireAPIKey returns middleware that enforces the X-API-Key header against
// apiKeyHashHex (sha256 hex of the key). Comparison is constant-time. It fails
// CLOSED: if no hash is configured, or the header is missing/wrong, it 401s.
func RequireAPIKey(apiKeyHashHex string) func(http.Handler) http.Handler {
	want, _ := hex.DecodeString(apiKeyHashHex)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(want) == 0 {
				unauthorized(w)
				return
			}
			key := r.Header.Get("X-API-Key")
			if key == "" {
				unauthorized(w)
				return
			}
			sum := sha256.Sum256([]byte(key))
			if subtle.ConstantTimeCompare(sum[:], want) != 1 {
				unauthorized(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHORIZED","message":"invalid or missing API key"}}`))
}
