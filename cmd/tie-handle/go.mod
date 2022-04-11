module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20220411153446-ffb8e92ef0f0
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220411153446-ffb8e92ef0f0
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20220411153446-ffb8e92ef0f0 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20220408201424-a24fb2fb8a0f // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
