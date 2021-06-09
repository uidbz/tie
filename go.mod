module git.sr.ht/~uid/tie

go 1.16

require (
	git.sr.ht/~uid/putlib v0.0.0-20210504180720-4aba3cf34713
	git.sr.ht/~uid/tie/client v0.0.0-20210606173009-dd352469fcb8
	git.sr.ht/~uid/tie/request v0.0.0-20210609071741-dcd5d7fe16b1
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210609071741-dcd5d7fe16b1
	github.com/spf13/cobra v1.1.3
	gopkg.in/resty.v1 v1.12.0
)

replace git.sr.ht/~uid/tie/client => ./client

replace git.sr.ht/~uid/tie/request => ./request

replace git.sr.ht/~uid/tie/tiedb => ./tiedb
