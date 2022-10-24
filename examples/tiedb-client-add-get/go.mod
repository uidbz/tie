module tiedb-client-add-get

go 1.17

require (
	git.sr.ht/~uid/tie/client v0.0.0-20220604100723-7141445d2e5e
	git.sr.ht/~uid/tie/request v0.0.0-20220604100723-7141445d2e5e
)

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20221024072707-e9d3c2d8e9bf // indirect
	git.sr.ht/~uid/tie/metadata v0.0.0-20221024072707-e9d3c2d8e9bf // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20221024072707-e9d3c2d8e9bf // indirect
	github.com/go-resty/resty/v2 v2.7.0 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/net v0.1.0 // indirect
	golang.org/x/sys v0.1.0 // indirect
)

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/request => ../../request
