module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20210927122737-608e131cb3d2
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211004073741-504d6e3f201d
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20211004073741-504d6e3f201d // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20211003122950-b1ebd4e1001c // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
