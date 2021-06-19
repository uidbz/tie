#!/bin/bash
git add -A
git commit -m "$@"
git push

./update-modules.sh

git add -A
git commit -m "Update modules to latest versions"
git push
