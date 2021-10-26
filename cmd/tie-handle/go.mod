module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20211021072118-10d9aee6037e
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211021072118-10d9aee6037e
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20211021072118-10d9aee6037e // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20211025201205-69cdffdb9359 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
