module tiedb-client-batch

go 1.17

require (
	git.sr.ht/~uid/tie/client v0.0.0-20220417162521-6ab633f69d4d
	git.sr.ht/~uid/tie/request v0.0.0-20220417162521-6ab633f69d4d
)

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220417162521-6ab633f69d4d // indirect
	git.sr.ht/~uid/tie/metadata v0.0.0-20220417162521-6ab633f69d4d // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220417162521-6ab633f69d4d // indirect
	github.com/go-resty/resty/v2 v2.7.0 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/net v0.0.0-20220412020605-290c469a71a5 // indirect
	golang.org/x/sys v0.0.0-20220412211240-33da011f77ad // indirect
)

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/request => ../../request
