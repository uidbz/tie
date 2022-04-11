module tiedb-client-batch

go 1.17

require (
	git.sr.ht/~uid/tie/client v0.0.0-20220410141147-8046611e3de9
	git.sr.ht/~uid/tie/request v0.0.0-20220410141147-8046611e3de9
)

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220410141147-8046611e3de9 // indirect
	git.sr.ht/~uid/tie/metadata v0.0.0-20220410141147-8046611e3de9 // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220410141147-8046611e3de9 // indirect
	github.com/go-resty/resty/v2 v2.7.0 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/net v0.0.0-20220407224826-aac1ed45d8e3 // indirect
	golang.org/x/sys v0.0.0-20220408201424-a24fb2fb8a0f // indirect
)

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/request => ../../request
