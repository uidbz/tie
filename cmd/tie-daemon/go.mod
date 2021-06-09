module git.sr.ht/~uid/tie-daemon

go 1.16

require (
	git.sr.ht/~uid/tie/request v0.0.0-20210606173009-dd352469fcb8
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210606173009-dd352469fcb8
	github.com/julienschmidt/httprouter v1.3.0
	github.com/yuin/goldmark v1.3.7
)

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb

replace git.sr.ht/~uid/tie/request => ../../request
