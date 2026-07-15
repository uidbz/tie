#!/usr/bin/env sh

root=$(pwd)
output_dir=$root/dist/

echo "Build directory:" $output_dir

build_cmd() {
    echo "Building cmd..."
    
    cd $root/cmd/tie
    go build  -o $output_dir

    cd $root/cmd/tie-daemon
    go build  -o $output_dir

    cd $root/cmd/tie-handle
    go build  -o $output_dir

    cd $root/cmd/tie-filehost
    go build  -o $output_dir
}

build_cmd
