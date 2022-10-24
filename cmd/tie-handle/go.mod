module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20221024072707-e9d3c2d8e9bf
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20221024072707-e9d3c2d8e9bf
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20221024072707-e9d3c2d8e9bf // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.1.0 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
