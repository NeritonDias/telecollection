// Package httpapi holds the HTTP handlers shared by desktop (Wails AssetServer.Handler)
// and server (daemon) modes. Handlers here are transport-only; domain logic lives in
// the internal/* domain packages.
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/telecollection/telecollection/internal/version"
)

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// Health responds with service liveness and version. No authentication required —
// this is the only unauthenticated endpoint (mirrors the REST API contract).
func Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(healthResponse{
		Status:  "ok",
		Version: version.String(),
	})
}
