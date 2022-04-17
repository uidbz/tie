module git.sr.ht/~uid/tie/cmd/tie-daemon

go 1.16

require (
	git.sr.ht/~uid/tie/request v0.0.0-20220417162521-6ab633f69d4d
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220417162521-6ab633f69d4d
	github.com/julienschmidt/httprouter v1.3.0
	github.com/yuin/goldmark v1.4.11
)

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb

replace git.sr.ht/~uid/tie/request => ../../request
