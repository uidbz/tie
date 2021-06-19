module git.sr.ht/~uid/tie

go 1.16

require (
	git.sr.ht/~uid/putlib v0.0.0-20210504180720-4aba3cf34713
	git.sr.ht/~uid/tie/client v0.0.0-20210619184020-b0affc015bfc
	git.sr.ht/~uid/tie/request v0.0.0-20210619183836-21eb3cdfb30a
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210619183836-21eb3cdfb30a
	github.com/spf13/cobra v1.1.3
	golang.org/x/sys v0.0.0-20210616094352-59db8d763f22 // indirect
	gopkg.in/resty.v1 v1.12.0
)

replace git.sr.ht/~uid/tie/client => ./client

replace git.sr.ht/~uid/tie/request => ./request

replace git.sr.ht/~uid/tie/tiedb => ./tiedb
