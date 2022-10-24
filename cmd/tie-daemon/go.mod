module git.sr.ht/~uid/tie/cmd/tie-daemon

go 1.16

require (
	git.sr.ht/~uid/tie/request v0.0.0-20220604100723-7141445d2e5e
	git.sr.ht/~uid/tie/tiedb v0.0.0-20221024072707-e9d3c2d8e9bf
	github.com/julienschmidt/httprouter v1.3.0
	github.com/yuin/goldmark v1.5.2
)

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb

replace git.sr.ht/~uid/tie/request => ../../request
