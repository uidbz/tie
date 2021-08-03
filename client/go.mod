module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210718195051-40553d651154
	git.sr.ht/~uid/tie/metadata v0.0.0-20210803200652-7f07e46d5ebd
	git.sr.ht/~uid/tie/request v0.0.0-20210803200652-7f07e46d5ebd
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210803200652-7f07e46d5ebd
	golang.org/x/net v0.0.0-20210726213435-c6fcb2dbf985 // indirect
	gopkg.in/resty.v1 v1.12.0
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
