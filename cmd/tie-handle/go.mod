module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20220403213716-e60d780c3cf2
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220403213716-e60d780c3cf2
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20220403213716-e60d780c3cf2 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20220403205710-6acee93ad0eb // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
