module git.sr.ht/~uid/tie

go 1.16

require (
	git.sr.ht/~uid/tie/client v0.0.0-20210927122737-608e131cb3d2
	git.sr.ht/~uid/tie/request v0.0.0-20210927122737-608e131cb3d2
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211004073741-504d6e3f201d
	github.com/spf13/cobra v1.2.1
)

replace git.sr.ht/~uid/tie/client => ./client

replace git.sr.ht/~uid/tie/request => ./request

replace git.sr.ht/~uid/tie/tiedb => ./tiedb

replace git.sr.ht/~uid/tie/io/putlib => ./io/putlib
