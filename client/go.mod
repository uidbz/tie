module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220415093055-65579d4670d9
	git.sr.ht/~uid/tie/request v0.0.0-20220415093055-65579d4670d9
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220415093055-65579d4670d9
	github.com/go-resty/resty/v2 v2.7.0
	golang.org/x/net v0.0.0-20220412020605-290c469a71a5 // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
