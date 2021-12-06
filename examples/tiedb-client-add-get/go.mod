module tiedb-client-add-get

go 1.17

require (
	git.sr.ht/~uid/tie/client v0.0.0-20211203084034-9d28a8705a27
	git.sr.ht/~uid/tie/request v0.0.0-20211203084034-9d28a8705a27
)

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211203084034-9d28a8705a27 // indirect
	git.sr.ht/~uid/tie/metadata v0.0.0-20211203084034-9d28a8705a27 // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211203084034-9d28a8705a27 // indirect
	github.com/go-resty/resty/v2 v2.7.0 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/net v0.0.0-20211203184738-4852103109b8 // indirect
	golang.org/x/sys v0.0.0-20211124211545-fe61309f8881 // indirect
)

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/request => ../../request