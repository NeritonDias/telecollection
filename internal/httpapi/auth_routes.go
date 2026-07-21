package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/telecollection/telecollection/internal/telegram/auth"
)

// RegisterAuthRoutes mounts the Telegram login endpoints onto r. It depends only
// on the auth.Service contract, so it can be wired ahead of the real flow.
//
// Routes (relative to the mount point):
//
//	POST /auth/start    {"phone":"..."}    -> 200 {"state":"wait_code"}
//	POST /auth/code     {"code":"..."}     -> 200 {"state":"wait_password"|"logged_in"}
//	POST /auth/password {"password":"..."} -> 200 {"state":"logged_in"}
//	POST /auth/qr                          -> 200 {"qr_url":"tg://login?token=..."}
//	GET  /auth/status                      -> 200 {"state":"..."}
//	POST /auth/logout                      -> 204
func RegisterAuthRoutes(r chi.Router, svc auth.Service) {
	r.Post("/auth/start", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Phone string `json:"phone"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if err := svc.StartLogin(r.Context(), body.Phone); err != nil {
			writeServiceError(w, err)
			return
		}
		// StartLogin returns nil both when a code is awaited and when a valid
		// session already made the login unnecessary. Report the real state so a
		// returning (already-authorized) user isn't told to enter a code.
		st, _ := svc.Status(r.Context())
		writeState(w, st)
	})

	r.Post("/auth/code", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		err := svc.SubmitCode(r.Context(), body.Code)
		switch {
		case err == nil:
			writeState(w, auth.StateLoggedIn)
		case errors.Is(err, auth.ErrPasswordNeeded):
			writeState(w, auth.StateWaitPassword)
		default:
			writeServiceError(w, err)
		}
	})

	r.Post("/auth/password", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Password string `json:"password"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if err := svc.SubmitPassword(r.Context(), body.Password); err != nil {
			writeServiceError(w, err)
			return
		}
		writeState(w, auth.StateLoggedIn)
	})

	r.Post("/auth/qr", func(w http.ResponseWriter, r *http.Request) {
		qrURL, err := svc.StartQR(r.Context())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"qr_url": qrURL})
	})

	r.Get("/auth/status", func(w http.ResponseWriter, r *http.Request) {
		state, err := svc.Status(r.Context())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeState(w, state)
	})

	r.Post("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if err := svc.Logout(r.Context()); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// decodeBody parses the JSON request body into dst. On failure it writes a 400
// error response and returns false. Secrets in the body are never logged.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return false
	}
	return true
}

func writeState(w http.ResponseWriter, state auth.State) {
	writeJSON(w, http.StatusOK, map[string]string{"state": string(state)})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeServiceError maps a business error to a 500 JSON envelope. The error
// message is generic on the wire so no secret (phone/code/password) leaks.
func writeServiceError(w http.ResponseWriter, _ error) {
	writeError(w, http.StatusInternalServerError, "AUTH_ERROR", "authentication request failed")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
