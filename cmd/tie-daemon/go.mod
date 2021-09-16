module git.sr.ht/~uid/tie/cmd/tie-daemon

go 1.16

require (
	git.sr.ht/~uid/tie/request v0.0.0-20210916070403-d64f099eb7da
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210916070403-d64f099eb7da
	github.com/julienschmidt/httprouter v1.3.0
	github.com/yuin/goldmark v1.4.1
)

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb

replace git.sr.ht/~uid/tie/request => ../../request
