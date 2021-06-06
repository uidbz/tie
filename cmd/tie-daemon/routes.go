package main

import (
	"github.com/julienschmidt/httprouter"
)

func routes(r *httprouter.Router) {
	r.GET("/", Index)
	r.GET("/:database/:collection/:type/:id", GetHandler)
	r.GET("/:database/:collection/:type/:id/:selection", GetHandler)
	r.POST("/:database/:collection/:type", RequestHandler)
}
