module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie v0.0.0-20210511205824-eda5c64cdc10 // indirect
	git.sr.ht/~uid/tie/request v0.0.0-20210511205824-eda5c64cdc10 // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210511205824-eda5c64cdc10 // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/putlib => ../tiedb
