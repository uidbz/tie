module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20220112073942-66ae131b3d6d
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220112073942-66ae131b3d6d
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20220112073942-66ae131b3d6d // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20220111092808-5a964db01320 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
