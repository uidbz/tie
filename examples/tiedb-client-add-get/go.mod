module tiedb-client-add-get

go 1.17

require (
	git.sr.ht/~uid/tie/client v0.0.0-20220205171529-6a3d524edd61
	git.sr.ht/~uid/tie/request v0.0.0-20220205171529-6a3d524edd61
)

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220112201315-398bccc5acac // indirect
	git.sr.ht/~uid/tie/metadata v0.0.0-20220112201315-398bccc5acac // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220205171529-6a3d524edd61 // indirect
	github.com/go-resty/resty/v2 v2.7.0 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/net v0.0.0-20220127200216-cd36cc0744dd // indirect
	golang.org/x/sys v0.0.0-20220204135822-1c1b9b1eba6a // indirect
)

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/request => ../../request
