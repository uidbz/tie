package main

import (
	"net/http"

	"github.com/uidbz/tie/auth"
)

func routes(r *http.ServeMux, store *auth.Store) {
	// Under /-/ so it cannot collide with the single-segment /{hash} routes
	// (a GET pattern also serves HEAD, which the mux would flag against
	// "HEAD /{hash}").
	r.HandleFunc("GET /-/version", store.Require(auth.AccessRead, VersionHandler))
	r.HandleFunc("GET /{hash}", store.Require(auth.AccessRead, DownloadHandler))
	r.HandleFunc("HEAD /{hash}", store.Require(auth.AccessRead, StatHandler))
	r.HandleFunc("GET /{hash}/{filename}", store.Require(auth.AccessRead, NamedDownloadHandler))
	r.HandleFunc("PUT /upload", store.Require(auth.AccessWrite, UploadHandler))
	r.HandleFunc("PUT /upload/{hash}", store.Require(auth.AccessWrite, UploadHandler))
	r.HandleFunc("PUT /retention/{hash}", store.Require(auth.AccessWrite, SetRetentionHandler))
	r.HandleFunc("GET /retention/{hash}", store.Require(auth.AccessRead, GetRetentionHandler))
}
