module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20210918071337-fd0d8dd9ae0e
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210918071337-fd0d8dd9ae0e
)

require (
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20210923061019-b8560ed6a9b7 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
