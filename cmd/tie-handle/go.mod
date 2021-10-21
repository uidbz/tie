module tie-handle

go 1.17

require (
	git.sr.ht/~uid/tie/io/getlib v0.0.0-20211013062036-447ddea6c8e2
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211013062036-447ddea6c8e2
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20211013062036-447ddea6c8e2 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/sys v0.0.0-20211020174200-9d6173849985 // indirect
)

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
