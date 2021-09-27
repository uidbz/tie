module git.sr.ht/~uid/tie

go 1.16

require (
	git.sr.ht/~uid/tie/client v0.0.0-20210925203241-732d1eace6c7
	git.sr.ht/~uid/tie/request v0.0.0-20210925203241-732d1eace6c7
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210925203241-732d1eace6c7
	github.com/spf13/cobra v1.2.1
)

replace git.sr.ht/~uid/tie/client => ./client

replace git.sr.ht/~uid/tie/request => ./request

replace git.sr.ht/~uid/tie/tiedb => ./tiedb

replace git.sr.ht/~uid/tie/io/putlib => ./io/putlib
