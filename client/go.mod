module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210829082918-7d33cf75f27b
	git.sr.ht/~uid/tie/metadata v0.0.0-20210829082918-7d33cf75f27b
	git.sr.ht/~uid/tie/request v0.0.0-20210829082918-7d33cf75f27b
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210829082918-7d33cf75f27b
	github.com/go-resty/resty/v2 v2.6.0
	golang.org/x/net v0.0.0-20210825183410-e898025ed96a // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
