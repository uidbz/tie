package main

import (
	"flag"
	"fmt"
	"os"

	"git.sr.ht/~uid/tie/api"
	"git.sr.ht/~uid/tie/tiedb"
	"git.sr.ht/~uid/tie/webservice"
	"github.com/julienschmidt/httprouter"
)

var (
	__VERBOSE bool = true
	__DEBUG   bool = true

	db     *tiedb.TieTree
	dbPath string
)

func main() {
	var insecure = flag.Bool("insecure", false, "Use HTTP instead of HTTPS.")
	var certFile = flag.String("tls-cert", "", "Root certificate filename.")
	var keyFile = flag.String("tls-key", "", "Private key filename.")
	var addr = flag.String("listen", ":1161", "Listen on particular address/port (ignored if using certmagic).")
	var useCertmagic = flag.Bool("certmagic", false, "Use Let's encrypt for TLS certificate")
	var certmagicHost = flag.String("host", "", "Hostname for certmagic")
	var path = flag.String("db-path", "", "Databases path.")

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
		DbPath:        *path,
		// Only these relations are ever queried in reverse (tag lookups, path->UID,
		// and UID children via parent). Restricting the reverse index to them keeps
		// the bulk of file metadata (filename, size, media-type, ...) from doubling
		// association memory.
		ReverseRelations: []string{"tag", "path", "parent"},
	}

	c := api.CollectionInfo{}

	requests := []webservice.RequestInterface{
		c.NewDummyRequest(),
		c.NewGetRequest(""),
		c.NewAddRequest("", "", ""),
		c.NewDeleteRequest("", "", ""),
		c.NewUpdateRequest(api.Update{}),
		api.NewBatchRequest(&api.Batch{}),
		c.NewAssociatedRequest(""),
		c.NewSyncRequest(),
		c.NewDumpRequest(),
	}

	ws := webservice.NewWebservice(config, requests)

	routes := func(r *httprouter.Router) {
		r.POST("/:request", ws.BasicAuth(ws.RequestHandler))
	}

	ws.AddUser("defaultuser", "defaultpassword")

	ws.ListenAndServe(routes)
}
