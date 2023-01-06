module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20230106084800-144edbfaae3b
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20221024072834-4e948d6ac84e
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20221024072834-4e948d6ac84e // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.4.0 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
