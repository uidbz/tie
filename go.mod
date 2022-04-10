module git.sr.ht/~uid/tie

go 1.16

require (
	git.sr.ht/~uid/tie/client v0.0.0-20220410090907-a8464ea42d4b
	git.sr.ht/~uid/tie/request v0.0.0-20220410090907-a8464ea42d4b
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220410090907-a8464ea42d4b
	github.com/spf13/cobra v1.4.0
)

replace git.sr.ht/~uid/tie/client => ./client

replace git.sr.ht/~uid/tie/request => ./request

replace git.sr.ht/~uid/tie/tiedb => ./tiedb

replace git.sr.ht/~uid/tie/io/putlib => ./io/putlib
