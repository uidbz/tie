module git.sr.ht/~uid/tie/cmd/tie-serve

go 1.16

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20210913162750-3bfb3f58467e
	github.com/dhowden/tag v0.0.0-20201120070457-d52dcb253c63
	github.com/h2non/filetype v1.1.1
	github.com/julienschmidt/httprouter v1.3.0
	github.com/minio/highwayhash v1.0.2
	golang.org/x/sys v0.0.0-20210910150752-751e447fb3d0 // indirect
)

replace git.sr.ht/~uid/tie/metadata => ../../metadata
