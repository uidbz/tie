#!/usr/bin/env sh

go get -u .
go mod tidy

git add -A
git commit -m "$@"
git push
