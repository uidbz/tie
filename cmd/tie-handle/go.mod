module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20220417162521-6ab633f69d4d
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220417162521-6ab633f69d4d
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20220417162521-6ab633f69d4d // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20220412211240-33da011f77ad // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
