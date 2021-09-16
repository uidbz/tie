module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20210916070403-d64f099eb7da
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210916070403-d64f099eb7da
)

require (
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20210915083310-ed5796bab164 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
