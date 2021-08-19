module git.sr.ht/~uid/tie

go 1.16

require (
	git.sr.ht/~uid/tie/client v0.0.0-20210818074128-d0875899865f
	git.sr.ht/~uid/tie/request v0.0.0-20210818074128-d0875899865f
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210818074128-d0875899865f
	github.com/spf13/cobra v1.2.1
)

replace git.sr.ht/~uid/tie/client => ./client

replace git.sr.ht/~uid/tie/request => ./request

replace git.sr.ht/~uid/tie/tiedb => ./tiedb

replace git.sr.ht/~uid/tie/io/putlib => ./io/putlib
