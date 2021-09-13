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

    cd $root/cmd/tie-download
    go build  -o $output_dir

    cd $root/cmd/tie-upload
    go build  -o $output_dir

    cd $root/cmd/tie-upload
    go build  -o $output_dir

    cd $root/cmd/tie-serve
    go build  -o $output_dir
}

build_gui() {
    echo "Building gui..."
    
    cd $root/gui/tie-tag
    go build  -o $output_dir

}

case "$1" in
"cmd")
    build_cmd
    ;;

"gui")
    build_gui
    ;;
    
"")
    build_cmd
    build_gui
    
esac
