#!/bin/bash
git add -A
git commit -m "$@"
git push
cd tiedb
go get -u .
cd ../metadata
go get -u .
cd ../request
go get -u .
go get git.sr.ht/~uid/tie/tiedb@latest
cd ../client
go get -u .
go get git.sr.ht/~uid/tie/tiedb@latest
go get git.sr.ht/~uid/tie/metadata@latest
go get git.sr.ht/~uid/tie/client@latest
cd ../cmd/tie
go get -u .
go get git.sr.ht/~uid/putlib@latest
go get git.sr.ht/~uid/tie/client@latest
go get git.sr.ht/~uid/tie/request@latest
go get git.sr.ht/~uid/tie/tiedb@latest
cd ../tie-daemon
go get -u .
go get git.sr.ht/~uid/tie/request@latest
go get git.sr.ht/~uid/tie/tiedb@latest
cd ../../gui/component
go get -u .
go get git.sr.ht/~uid/tie/client@latest
go get git.sr.ht/~uid/tie/request@latest
cd ../tie-tag
go get -u .
go get git.sr.ht/~uid/tie/component@latest
go get git.sr.ht/~uid/tie/client@latest
go get git.sr.ht/~uid/putlib@latest
git add -A
git commit -m "Update modules to latest versions"
git push
