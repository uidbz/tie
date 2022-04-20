module tiedb-client-add-get

go 1.17

require (
	git.sr.ht/~uid/tie/client v0.0.0-20220417162659-7bbeb6d035c3
	git.sr.ht/~uid/tie/request v0.0.0-20220417162659-7bbeb6d035c3
)

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220417162659-7bbeb6d035c3 // indirect
	git.sr.ht/~uid/tie/metadata v0.0.0-20220417162659-7bbeb6d035c3 // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220417162659-7bbeb6d035c3 // indirect
	github.com/go-resty/resty/v2 v2.7.0 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/net v0.0.0-20220420153159-1850ba15e1be // indirect
	golang.org/x/sys v0.0.0-20220412211240-33da011f77ad // indirect
)

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/request => ../../request
