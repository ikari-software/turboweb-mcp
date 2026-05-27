package main

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

// daemonVersionInfo is what /version returns on the daemon's HTTP port.
// It carries enough fingerprinting that a freshly-spawned MCP instance
// can decide whether the daemon already running is the same build, a
// stale build, or someone else's process listening on our port.
type daemonVersionInfo struct {
	Service       string    `json:"service"`              // sentinel "turboweb-mcp"
	Version       string    `json:"version"`              // server-version string
	PID           int       `json:"pid"`                  // daemon PID
	BinaryPath    string    `json:"binaryPath,omitempty"` // resolved os.Executable() at startup
	BinaryModTime time.Time `json:"binaryModTime"`        // mtime of the binary file at startup
	StartedAt     time.Time `json:"startedAt"`            // daemon process start time
}

// daemonInfo captured at daemon startup; serves /version reads after.
var daemonInfo daemonVersionInfo

// initDaemonInfo populates daemonInfo with the current process's
// binary fingerprint. Must run before the /version handler is registered.
func initDaemonInfo() {
	daemonInfo.Service = "turboweb-mcp"
	daemonInfo.Version = serverVersion
	daemonInfo.PID = os.Getpid()
	daemonInfo.StartedAt = time.Now().UTC()
	if exe, err := os.Executable(); err == nil {
		daemonInfo.BinaryPath = exe
		if st, err := os.Stat(exe); err == nil {
			daemonInfo.BinaryModTime = st.ModTime().UTC()
		}
	}
}

// handleVersion serves a JSON snapshot of daemonInfo. Cheap, no auth —
// it's already gated by localhost-only binding.
func handleVersion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(daemonInfo)
}

// handleReloadExtension broadcasts a reload_extension message to every
// connected browser extension. Triggered by POST (or GET) /reload-extension.
// Gated by localhost-only binding — no additional auth needed.
func handleReloadExtension(w http.ResponseWriter, _ *http.Request) {
	broadcastExtensionReload(serverVersion)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"version": serverVersion,
		"message": "reload_extension broadcast sent to all connected browsers",
	})
}
