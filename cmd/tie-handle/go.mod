module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20220410140021-5c1999095f73
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220410140021-5c1999095f73
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20220410140021-5c1999095f73 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20220408201424-a24fb2fb8a0f // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
