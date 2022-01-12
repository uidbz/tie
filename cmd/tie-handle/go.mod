module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20211206214737-0d583b9f07b4
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211206214737-0d583b9f07b4
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20211206214737-0d583b9f07b4 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20220111092808-5a964db01320 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
