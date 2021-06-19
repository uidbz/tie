module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210504180720-4aba3cf34713
	git.sr.ht/~uid/tie/metadata v0.0.0-20210606173009-dd352469fcb8
	git.sr.ht/~uid/tie/request v0.0.0-20210609071741-dcd5d7fe16b1
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210609071741-dcd5d7fe16b1
	golang.org/x/net v0.0.0-20210525063256-abc453219eb5 // indirect
	golang.org/x/sys v0.0.0-20210608053332-aa57babbf139 // indirect
	gopkg.in/resty.v1 v1.12.0
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
