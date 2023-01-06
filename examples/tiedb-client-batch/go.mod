module tiedb-client-batch

go 1.17

require (
	git.sr.ht/~uid/tie/client v0.0.0-20221024072834-4e948d6ac84e
	git.sr.ht/~uid/tie/request v0.0.0-20221024072834-4e948d6ac84e
)

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20221024072834-4e948d6ac84e // indirect
	git.sr.ht/~uid/tie/metadata v0.0.0-20221024072834-4e948d6ac84e // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20221024072834-4e948d6ac84e // indirect
	github.com/go-resty/resty/v2 v2.7.0 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/net v0.5.0 // indirect
	golang.org/x/sys v0.4.0 // indirect
)

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/request => ../../request
