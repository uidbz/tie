module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20210924183216-63054f9bcd07
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210924183216-63054f9bcd07
)

require (
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20210925032602-92d5a993a665 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
