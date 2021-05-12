module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/putlib v0.0.0-20210504180720-4aba3cf34713
	git.sr.ht/~uid/tie/metadata v0.0.0-20210512062001-da426b56a410
	git.sr.ht/~uid/tie/request v0.0.0-20210511205824-eda5c64cdc10
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210511205824-eda5c64cdc10
	gopkg.in/resty.v1 v1.12.0
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/putlib => ../tiedb
