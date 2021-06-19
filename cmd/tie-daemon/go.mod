module git.sr.ht/~uid/tie-daemon

go 1.16

require (
	git.sr.ht/~uid/tie/request v0.0.0-20210619183836-21eb3cdfb30a
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210619183836-21eb3cdfb30a
	github.com/julienschmidt/httprouter v1.3.0
	github.com/yuin/goldmark v1.3.8
)

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb

replace git.sr.ht/~uid/tie/request => ../../request
