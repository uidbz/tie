module tiedb-client-add-get

go 1.17

require (
	git.sr.ht/~uid/tie/client v0.0.0-20220112073942-66ae131b3d6d
	git.sr.ht/~uid/tie/request v0.0.0-20220112073942-66ae131b3d6d
)

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220112073942-66ae131b3d6d // indirect
	git.sr.ht/~uid/tie/metadata v0.0.0-20220112073942-66ae131b3d6d // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220112073942-66ae131b3d6d // indirect
	github.com/go-resty/resty/v2 v2.7.0 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/net v0.0.0-20220111093109-d55c255bac03 // indirect
	golang.org/x/sys v0.0.0-20220111092808-5a964db01320 // indirect
)

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/request => ../../request
