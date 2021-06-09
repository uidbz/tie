module git.sr.ht/~uid/tie-daemon

go 1.16

require (
	git.sr.ht/~uid/tie/request v0.0.0-20210609071741-dcd5d7fe16b1
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210609071741-dcd5d7fe16b1
	github.com/julienschmidt/httprouter v1.3.0
	github.com/yuin/goldmark v1.3.7
)

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb

replace git.sr.ht/~uid/tie/request => ../../request
