module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210620074611-ca132b42db60
	git.sr.ht/~uid/tie/metadata v0.0.0-20210620074611-ca132b42db60
	git.sr.ht/~uid/tie/request v0.0.0-20210620074611-ca132b42db60
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210620074611-ca132b42db60
	golang.org/x/net v0.0.0-20210614182718-04defd469f4e // indirect
	gopkg.in/resty.v1 v1.12.0
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
