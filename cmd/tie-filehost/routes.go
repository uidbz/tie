package main

import (
	"log/slog"
	"net/http"

	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/metadata"
)

// withHash validates the {hash} path wildcard before the handler runs. Go's
// ServeMux matches on the escaped path but hands the wildcard to the handler
// *unescaped*, so `%2F` arrives as `/`; a bare length check let a 64-char
// traversal string reach PathFromHash and read arbitrary files. Validating
// with metadata.IsHexHash here closes every {hash} route by construction.
func withHash(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hash := r.PathValue("hash")
		if !metadata.IsHexHash(hash) {
			slog.Warn("invalid hash requested", "hash", hash)
			http.Error(w, "Invalid hash", http.StatusBadRequest)
			return
		}
		h(w, r)
	}
}

func routes(r *http.ServeMux, store *auth.Store) {
	// Under /-/ so it cannot collide with the single-segment /{hash} routes
	// (a GET pattern also serves HEAD, which the mux would flag against
	// "HEAD /{hash}").
	r.HandleFunc("GET /-/version", store.Require(auth.AccessRead, VersionHandler))
	r.HandleFunc("GET /{hash}", store.Require(auth.AccessRead, withHash(DownloadHandler)))
	r.HandleFunc("HEAD /{hash}", store.Require(auth.AccessRead, withHash(StatHandler)))
	r.HandleFunc("GET /{hash}/{filename}", store.Require(auth.AccessRead, withHash(NamedDownloadHandler)))
	r.HandleFunc("PUT /upload", store.Require(auth.AccessWrite, UploadHandler))
	r.HandleFunc("PUT /upload/{hash}", store.Require(auth.AccessWrite, withHash(UploadHandler)))
	r.HandleFunc("PUT /retention/{hash}", store.Require(auth.AccessWrite, withHash(SetRetentionHandler)))
	r.HandleFunc("GET /retention/{hash}", store.Require(auth.AccessRead, withHash(GetRetentionHandler)))
}
