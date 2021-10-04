module git.sr.ht/~uid/tie/cmd/tie-daemon

go 1.16

require (
	git.sr.ht/~uid/tie/request v0.0.0-20210927122737-608e131cb3d2
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211004073741-504d6e3f201d
	github.com/julienschmidt/httprouter v1.3.0
	github.com/yuin/goldmark v1.4.1
)

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb

replace git.sr.ht/~uid/tie/request => ../../request
