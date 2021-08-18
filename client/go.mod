module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210813071935-06d25e133f3e
	git.sr.ht/~uid/tie/metadata v0.0.0-20210813071935-06d25e133f3e
	git.sr.ht/~uid/tie/request v0.0.0-20210813071935-06d25e133f3e
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210813071935-06d25e133f3e
	github.com/go-resty/resty/v2 v2.6.0 // indirect
	golang.org/x/net v0.0.0-20210813160813-60bc85c4be6d // indirect
	gopkg.in/resty.v1 v1.12.0
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
