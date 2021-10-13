module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20211011072406-3d9072e169be
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211011072406-3d9072e169be
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20211011072406-3d9072e169be // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20211007075335-d3039528d8ac // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
