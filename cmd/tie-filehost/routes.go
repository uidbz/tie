package main

import (
	"net/http"

	"git.sr.ht/~uid/tie/auth"
)

func routes(r *http.ServeMux, store *auth.Store) {
	r.HandleFunc("GET /{hash}", store.Require(auth.AccessRead, DownloadHandler))
	r.HandleFunc("HEAD /{hash}", store.Require(auth.AccessRead, StatHandler))
	r.HandleFunc("GET /{hash}/{filename}", store.Require(auth.AccessRead, NamedDownloadHandler))
	r.HandleFunc("PUT /upload", store.Require(auth.AccessWrite, UploadHandler))
	r.HandleFunc("PUT /upload/{hash}", store.Require(auth.AccessWrite, UploadHandler))
	r.HandleFunc("PUT /retention/{hash}", store.Require(auth.AccessWrite, SetRetentionHandler))
	r.HandleFunc("GET /retention/{hash}", store.Require(auth.AccessRead, GetRetentionHandler))
}
