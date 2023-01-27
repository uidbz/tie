package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"git.sr.ht/~uid/tie/examples/webservice/api"

	"github.com/julienschmidt/httprouter"

	"git.sr.ht/~uid/tie/webservice"
)

func main() {
	var insecure = flag.Bool("insecure", false, "Use HTTP instead of HTTPS.")
	var certFile = flag.String("tls-cert", "", "Root certificate filename.")
	var keyFile = flag.String("tls-key", "", "Private key filename.")
	var addr = flag.String("listen", ":8080", "Listen on particular address/port (ignored if using certmagic).")
	var useCertmagic = flag.Bool("certmagic", false, "Use Let's encrypt for TLS certificate")
	var certmagicHost = flag.String("host", "", "Hostname for certmagic")

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
		UseCertmagic:  *useCertmagic,
		CertmagicHost: *certmagicHost,
		AuthNamespace: "authentication",
		UserNamespace: "userdata",
		AuthFile:      "db",
	}

	requests := []webservice.RequestInterface{
		api.NewDummyRequest(),
	}

	ws := webservice.NewWebservice(config, requests)

	b, err := os.ReadFile("mailsettings.json")
	if err != nil {
		log.Fatal("Error reading mail settings file:", err)
	}

	mail := webservice.MailSettings{}
	errMail := json.Unmarshal(b, &mail)
	if errMail != nil {
		log.Fatal("Error unmarshalling mail settings file:", errMail)
	}

	ws.SetMailSettings(mail)

	routes := func(r *httprouter.Router) {
		r.POST("/:request", ws.BasicAuth(ws.RequestHandler))
	}

	ws.AddUser("myuser", "mypassword")

	ws.ListenAndServe(routes)
}
