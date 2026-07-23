package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"git.sr.ht/~uid/tie/examples/webservice/api"

	"git.sr.ht/~uid/tie/webservice"
)

func main() {
	var insecure = flag.Bool("insecure", false, "Use HTTP instead of HTTPS.")
	var certFile = flag.String("tls-cert", "", "Root certificate filename.")
	var keyFile = flag.String("tls-key", "", "Private key filename.")
	var addr = flag.String("listen", ":8080", "Listen on particular address/port.")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(os.Stderr, `Example webservice server
-----------------------------
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	config := webservice.WebserviceConfig{
		ListenOn:      *addr,
		Insecure:      *insecure,
		CertFile:      *certFile,
		KeyFile:       *keyFile,
		UserNamespace: "userdata",
		Users:         map[string]string{"myuser": "mypassword"},
	}

	requests := []webservice.RequestInterface{
		api.NewDummyRequest(),
	}

	ws := webservice.NewWebservice(config, requests)

	routes := func(mux *http.ServeMux) {
		mux.HandleFunc("POST /{request}", ws.BasicAuth(ws.RequestHandler))
	}

	ws.ListenAndServe(routes)
}
