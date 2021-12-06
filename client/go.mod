module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211203084034-9d28a8705a27
	git.sr.ht/~uid/tie/request v0.0.0-20211203084034-9d28a8705a27
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211203084034-9d28a8705a27
	github.com/go-resty/resty/v2 v2.7.0
	golang.org/x/net v0.0.0-20211205041911-012df41ee64c // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
