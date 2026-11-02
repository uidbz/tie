package server

// Static file publishing: a runner can publish a directory (client
// binaries, CA certs, artifacts) so hosts need no second web server.
// The directory is served twice when enabled: under /files/ on the main
// mux (the URL shape rollout tooling assumes), and — optionally — at
// the root of a plain HTTP listener for installs that hardcode that
// shape.

import (
	"log/slog"
	"net/http"
	"os"
)

// FileServerConfig configures static publishing. Startup-only: changing
// it takes a restart.
type FileServerConfig struct {
	Enabled bool
	// Dir is the published directory.
	Dir string
	// HTTPAddress optionally also serves Dir at the ROOT of a plain HTTP
	// listener (e.g. ":8080"). Empty = /files/ route only.
	HTTPAddress string
}

// MountFileServer serves Dir under /files/ on the given mux. ServeMux
// longest-match wins, so it coexists with any catch-all route and the
// /service/* data plane.
func MountFileServer(mux *http.ServeMux, cfg FileServerConfig) {
	if !cfg.Enabled || cfg.Dir == "" {
		return
	}
	mux.Handle("/files/", http.StripPrefix("/files/", http.FileServer(http.Dir(cfg.Dir))))
}

// StartPlainFileServer serves Dir at the root of a plain HTTP
// listener. A bind failure logs loudly and exits: a runner that
// silently loses the fleet's download port breaks every build that
// fetches from it, which is worse than failing at startup.
func StartPlainFileServer(cfg FileServerConfig) {
	if !cfg.Enabled || cfg.Dir == "" || cfg.HTTPAddress == "" {
		return
	}
	server := &http.Server{
		Addr:    cfg.HTTPAddress,
		Handler: http.FileServer(http.Dir(cfg.Dir)),
	}
	go func() {
		slog.Info("File server starting",
			"dir", cfg.Dir, "http_address", cfg.HTTPAddress, "tls_path", "/files/")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("File server error", "address", cfg.HTTPAddress, "error", err)
			os.Exit(1)
		}
	}()
}
