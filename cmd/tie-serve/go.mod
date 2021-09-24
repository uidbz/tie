module git.sr.ht/~uid/tie/cmd/tie-serve

go 1.16

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20210918071337-fd0d8dd9ae0e
	github.com/dhowden/tag v0.0.0-20201120070457-d52dcb253c63
	github.com/h2non/filetype v1.1.1
	github.com/julienschmidt/httprouter v1.3.0
	github.com/minio/highwayhash v1.0.2
	golang.org/x/crypto v0.0.0-20210921155107-089bfa567519
	golang.org/x/net v0.0.0-20210924151903-3ad01bbaa167 // indirect
	golang.org/x/sys v0.0.0-20210923061019-b8560ed6a9b7 // indirect
	golang.org/x/text v0.3.7 // indirect
)

replace git.sr.ht/~uid/tie/metadata => ../../metadata
