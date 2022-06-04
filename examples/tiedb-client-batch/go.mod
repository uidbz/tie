module tiedb-client-batch

go 1.17

require (
	git.sr.ht/~uid/tie/client v0.0.0-20220604100514-aa1375770049
	git.sr.ht/~uid/tie/request v0.0.0-20220604100514-aa1375770049
)

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220604100514-aa1375770049 // indirect
	git.sr.ht/~uid/tie/metadata v0.0.0-20220604100514-aa1375770049 // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220604100514-aa1375770049 // indirect
	github.com/go-resty/resty/v2 v2.7.0 // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	golang.org/x/net v0.0.0-20220531201128-c960675eff93 // indirect
	golang.org/x/sys v0.0.0-20220520151302-bc2c85ada10a // indirect
)

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/request => ../../request
