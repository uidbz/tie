module git.sr.ht/~uid/tie

go 1.16

require (
	git.sr.ht/~uid/putlib v0.0.0-20210504180720-4aba3cf34713
	git.sr.ht/~uid/tie/request v0.0.0-20210511205824-eda5c64cdc10 // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210511205824-eda5c64cdc10
	github.com/spf13/cobra v1.1.3
	golang.org/x/net v0.0.0-20210504132125-bbd867fde50d // indirect
	gopkg.in/resty.v1 v1.12.0
)

replace git.sr.ht/~uid/tie/request => ./request

replace git.sr.ht/~uid/tie/tiedb => ./tiedb
