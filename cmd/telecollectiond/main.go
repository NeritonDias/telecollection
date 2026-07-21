// Command telecollectiond is the headless TeleCollection daemon (server mode).
// In desktop mode the same handler is mounted on the Wails AssetServer.Handler
// (same-origin wails://) instead of a TCP listener.
package main

import (
	"log"
	"net/http"

	"github.com/telecollection/telecollection/internal/httpapi"
)

func main() {
	// Foundation stub: API key + origins get wired from config in a later phase.
	handler := httpapi.NewRouter(httpapi.Deps{})

	// Bind loopback only. Server mode fronts this with a reverse proxy / auth.
	const addr = "127.0.0.1:8550"
	log.Printf("telecollectiond listening on http://%s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil { //nolint:gosec // loopback dev stub; timeouts added with server hardening
		log.Fatal(err)
	}
}
