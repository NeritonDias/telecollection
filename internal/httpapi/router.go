package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/telecollection/telecollection/internal/drive"
	"github.com/telecollection/telecollection/internal/telegram/auth"
)

// Deps configures the router.
type Deps struct {
	APIKeyHashHex  string        // sha256 hex of the local API key; empty disables the API (fails closed)
	AllowedOrigins []string      // exact-match CORS origins (e.g. "wails://wails", "http://localhost:1420")
	Auth           auth.Service  // Telegram login flow; when nil the auth endpoints are not mounted
	Drive          drive.Service // folder/file operations; when nil the drive endpoints are not mounted
}

// NewRouter builds the HTTP handler shared by desktop and server modes.
// /health is public; everything under /api/v1 requires the API key.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(cors(d.AllowedOrigins))

	r.Get("/health", Health)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(RequireAPIKey(d.APIKeyHashHex))
		r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"pong":true}`))
		})
		r.Get("/events", sse) // SSE scaffold; real event bus arrives with transfers
		if d.Auth != nil {
			RegisterAuthRoutes(r, d.Auth)
		}
		if d.Drive != nil {
			RegisterDriveRoutes(r, d.Drive)
		}
	})

	return r
}

// cors allows only exact-match origins (no prefix matching — that was a bug in the
// original project). Credentials are not enabled.
func cors(allowed []string) func(http.Handler) http.Handler {
	set := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		set[o] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin := r.Header.Get("Origin"); origin != "" {
				if _, ok := set[origin]; ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Add("Vary", "Origin")
				}
			}
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func sse(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	_, _ = w.Write([]byte("event: hello\ndata: {}\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
