package main

import (
	"log/slog"
	"net/http"

	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/metadata"
)

// hashHandler handles a route whose {hash} wildcard has already been validated
// and whose blob has been located.
type hashHandler func(w http.ResponseWriter, r *http.Request, hash string, st *blobStore)

// withHash is the single gate for every route that addresses an existing blob:
// it validates the {hash} wildcard and resolves the store, answering 400 for a
// malformed hash and 404 when no store holds the blob. Go's ServeMux matches
// on the escaped path but hands the wildcard to the handler *unescaped*, so
// %2F arrives as "/"; a bare length check used to let a 64-char traversal
// string reach PathFromHash. Doing this here means a handler cannot forget it.
func (s *server) withHash(h hashHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hash := r.PathValue("hash")
		if !metadata.IsHexHash(hash) {
			slog.Warn("invalid hash requested", "hash", hash)
			http.Error(w, "Invalid hash", http.StatusBadRequest)
			return
		}
		st, ok := s.findBlob(hash)
		if !ok {
			http.NotFound(w, r)
			return
		}
		h(w, r, hash, st)
	}
}

// withValidHash validates the {hash} wildcard without requiring the blob to
// exist — for routes that address a blob that may not be stored yet (upload)
// or that answer 200 for absent state (a retention lookup).
func (s *server) withValidHash(h func(w http.ResponseWriter, r *http.Request, hash string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hash := r.PathValue("hash")
		if !metadata.IsHexHash(hash) {
			slog.Warn("invalid hash requested", "hash", hash)
			http.Error(w, "Invalid hash", http.StatusBadRequest)
			return
		}
		h(w, r, hash)
	}
}

// routes registers the filehost API. Everything under a {hash} wildcard goes
// through withHash/withValidHash, so hash validation is structural rather than
// a per-handler convention.
func (s *server) routes(mux *http.ServeMux, store *auth.Store) {
	// Under /-/ so it cannot collide with the single-segment /{hash} routes
	// (a GET pattern also serves HEAD, which the mux would flag against
	// "HEAD /{hash}").
	mux.HandleFunc("GET /-/version", store.Require(auth.AccessRead, s.versionHandler))
	mux.HandleFunc("GET /{hash}", store.Require(auth.AccessRead, s.withHash(s.downloadHandler)))
	mux.HandleFunc("HEAD /{hash}", store.Require(auth.AccessRead, s.withHash(s.statHandler)))
	mux.HandleFunc("GET /{hash}/{filename}", store.Require(auth.AccessRead, s.withHash(s.namedDownloadHandler)))
	mux.HandleFunc("PUT /upload", store.Require(auth.AccessWrite, func(w http.ResponseWriter, r *http.Request) {
		s.uploadHandler(w, r, "") // no {hash}: store-side address, computed while copying
	}))
	mux.HandleFunc("PUT /upload/{hash}", store.Require(auth.AccessWrite, s.withValidHash(s.uploadHandler)))
	mux.HandleFunc("PUT /retention/{hash}", store.Require(auth.AccessWrite, s.withHash(s.setRetentionHandler)))
	mux.HandleFunc("GET /retention/{hash}", store.Require(auth.AccessRead, s.withValidHash(s.getRetentionHandler)))
}
