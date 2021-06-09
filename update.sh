#!/bin/bash
git add -A
git commit -m "$@"
git push
cd tiedb
go get -u .
cd ../metadata
go get -u .
cd ../client
go get -u .
cd ../request
go get -u .
cd ../cmd/tie
go get -u .
cd ../tie-daemon
go get -u .
cd ../../gui/component
go get -u .
cd ../tie-tag
go get -u .
git add -A
git commit -m "Update modules to latest versions"
git push
