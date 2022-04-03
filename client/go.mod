module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220403213716-e60d780c3cf2
	git.sr.ht/~uid/tie/request v0.0.0-20220403213716-e60d780c3cf2
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220403213716-e60d780c3cf2
	github.com/go-resty/resty/v2 v2.7.0
	golang.org/x/net v0.0.0-20220403103023-749bd193bc2b // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
