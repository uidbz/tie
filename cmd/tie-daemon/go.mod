module git.sr.ht/~uid/tie/cmd/tie-daemon

go 1.16

require (
	git.sr.ht/~uid/tie/request v0.0.0-20210620074611-ca132b42db60
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210620074611-ca132b42db60
	github.com/julienschmidt/httprouter v1.3.0
	github.com/yuin/goldmark v1.3.8
)

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb

replace git.sr.ht/~uid/tie/request => ../../request
