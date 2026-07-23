package main

import (
	"net/http"
)

func routes(r *http.ServeMux) {
	r.HandleFunc("GET /{hash}", DownloadHandler)
	r.HandleFunc("GET /{hash}/{filename}", NamedDownloadHandler)
	r.HandleFunc("PUT /upload", UploadHandler)
	r.HandleFunc("PUT /upload/{hash}", UploadHandler)
	r.HandleFunc("PUT /retention/{hash}", SetRetentionHandler)
	r.HandleFunc("GET /retention/{hash}", GetRetentionHandler)
}
