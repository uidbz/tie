module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20211203083031-2aed8badebfd
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211026072142-3f546ca4c972
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20211026072142-3f546ca4c972 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20211124211545-fe61309f8881 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
