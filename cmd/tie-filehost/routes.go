package main

import (
	"github.com/julienschmidt/httprouter"
)

func routes(r *httprouter.Router) {
	r.GET("/:hash", DownloadHandler)
	r.GET("/:hash/:filename", NamedDownloadHandler)
	r.PUT("/upload", UploadHandler)
	r.PUT("/upload/:hash", UploadHandler)
	r.PUT("/upload/:hash/:json", UploadHandler)
}
