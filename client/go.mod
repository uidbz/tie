module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211013062036-447ddea6c8e2
	git.sr.ht/~uid/tie/request v0.0.0-20211013062036-447ddea6c8e2
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211013062036-447ddea6c8e2
	github.com/go-resty/resty/v2 v2.6.0
	golang.org/x/net v0.0.0-20211020060615-d418f374d309 // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
