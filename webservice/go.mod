module git.sr.ht/~uid/tie/webservice

go 1.16

require (
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220410141147-8046611e3de9
	github.com/caddyserver/certmagic v0.16.0
	github.com/go-resty/resty/v2 v2.7.0
	github.com/julienschmidt/httprouter v1.3.0
)

replace git.sr.ht/~uid/tie/tiedb => ../tiedb