module git.sr.ht/~uid/tie/cmd/tie-serve

go 1.16

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20210619185141-28fca9f10dad
	github.com/dhowden/tag v0.0.0-20201120070457-d52dcb253c63
	github.com/h2non/filetype v1.1.1
	github.com/julienschmidt/httprouter v1.3.0
	github.com/minio/highwayhash v1.0.2
	golang.org/x/sys v0.0.0-20210616094352-59db8d763f22 // indirect
)

replace git.sr.ht/~uid/tie/metadata => ../../metadata
