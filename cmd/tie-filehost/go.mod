module git.sr.ht/~uid/tie/cmd/tie-filehost

go 1.16

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20221024072707-e9d3c2d8e9bf
	github.com/caddyserver/certmagic v0.17.2
	github.com/dhowden/tag v0.0.0-20220618230019-adf36e896086
	github.com/h2non/filetype v1.1.3
	github.com/julienschmidt/httprouter v1.3.0
	github.com/klauspost/cpuid/v2 v2.1.2 // indirect
	github.com/minio/highwayhash v1.0.2
	go.uber.org/atomic v1.10.0 // indirect
	go.uber.org/multierr v1.8.0 // indirect
	golang.org/x/tools v0.2.0 // indirect
)

replace git.sr.ht/~uid/tie/metadata => ../../metadata
