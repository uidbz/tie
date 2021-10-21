module git.sr.ht/~uid/tie

go 1.16

require (
	git.sr.ht/~uid/tie/client v0.0.0-20211013062036-447ddea6c8e2
	git.sr.ht/~uid/tie/request v0.0.0-20211013062036-447ddea6c8e2
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211013062036-447ddea6c8e2
	github.com/spf13/cobra v1.2.1
)

replace git.sr.ht/~uid/tie/client => ./client

replace git.sr.ht/~uid/tie/request => ./request

replace git.sr.ht/~uid/tie/tiedb => ./tiedb

replace git.sr.ht/~uid/tie/io/putlib => ./io/putlib
