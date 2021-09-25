module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20210925084131-da14c14044cd
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210925084131-da14c14044cd
)

require (
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20210925032602-92d5a993a665 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
