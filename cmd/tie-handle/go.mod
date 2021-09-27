module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20210927095751-1917d6757f80
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210927095751-1917d6757f80
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20210927095751-1917d6757f80 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20210927094055-39ccf1dd6fa6 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
