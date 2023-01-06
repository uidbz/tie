module git.sr.ht/~uid/tie/cmd/tie-daemon

go 1.16

require (
	git.sr.ht/~uid/tie/request v0.0.0-20221024072834-4e948d6ac84e
	git.sr.ht/~uid/tie/tiedb v0.0.0-20221024072834-4e948d6ac84e
	github.com/julienschmidt/httprouter v1.3.0
	github.com/yuin/goldmark v1.5.3
)

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb

replace git.sr.ht/~uid/tie/request => ../../request
