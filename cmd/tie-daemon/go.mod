module git.sr.ht/~uid/tie/cmd/tie-daemon

go 1.16

require (
	git.sr.ht/~uid/tie/request v0.0.0-20211203083031-2aed8badebfd
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211026072142-3f546ca4c972
	github.com/julienschmidt/httprouter v1.3.0
	github.com/yuin/goldmark v1.4.4
)

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb

replace git.sr.ht/~uid/tie/request => ../../request
