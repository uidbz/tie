#!/usr/bin/env sh

echo "Updating tiedb..."
cd tiedb
go get -u .
go mod tidy

echo "Updating metadata..."
cd ../metadata
go get -u .
go mod tidy

echo "Updating putlib..."
cd ../io/putlib
go get -u .
go mod tidy

echo "Updating getlib..."
cd ../getlib
go get -u .
go mod tidy

echo "Updating request..."
cd ../../request
go get -u .
go get git.sr.ht/~uid/tie/tiedb@latest
go mod tidy

echo "Updating client..."
cd ../client
go get -u .
go get git.sr.ht/~uid/tie/tiedb@latest
go get git.sr.ht/~uid/tie/metadata@latest
go mod tidy

echo "Updating tie..."
cd ../cmd/tie
go get -u .
go get git.sr.ht/~uid/tie/io/putlib@latest
go get git.sr.ht/~uid/tie/client@latest
go get git.sr.ht/~uid/tie/request@latest
go get git.sr.ht/~uid/tie/tiedb@latest
go mod tidy

echo "Updating tie-handle..."
cd ../tie-handle
go get -u .
go get git.sr.ht/~uid/tie/io/putlib@latest
go get git.sr.ht/~uid/tie/io/getlib@latest
go mod tidy

echo "Updating tie-daemon..."
cd ../tie-daemon
go get -u .
go get git.sr.ht/~uid/tie/request@latest
go get git.sr.ht/~uid/tie/tiedb@latest
go mod tidy

echo "Updating gui component..."
cd ../../gui/component
go get -u .
go get git.sr.ht/~uid/tie/client@latest
go get git.sr.ht/~uid/tie/request@latest
go mod tidy

echo "Updating tie-tag..."
cd ../tie-tag
go get -u .
go get git.sr.ht/~uid/tie/gui/component@latest
go get git.sr.ht/~uid/tie/client@latest
go get git.sr.ht/~uid/putlib@latest
go mod tidy

echo "Updating tie-upload..."
cd ../../cmd/tie-upload
go get -u .
go get git.sr.ht/~uid/tie/io/putlib@latest
go mod tidy

echo "Updating tie-download..."
cd ../tie-download
go get -u .
go get git.sr.ht/~uid/tie/io/getlib@latest
go mod tidy

echo "Updating tie-serve..."
cd ../tie-serve
go get -u .
go get git.sr.ht/~uid/tie/metadata@latest
go mod tidy
