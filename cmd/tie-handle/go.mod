module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20210925203241-732d1eace6c7
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210925203241-732d1eace6c7
)

require (
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20210927052749-1cf2251ac284 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
