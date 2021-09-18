module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20210916073254-3c4e1d9904b3
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210916073254-3c4e1d9904b3
)

require (
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20210917161153-d61c044b1678 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
