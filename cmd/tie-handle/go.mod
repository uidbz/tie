module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20220205171529-6a3d524edd61
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220112201315-398bccc5acac
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20220112201315-398bccc5acac // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20220204135822-1c1b9b1eba6a // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
