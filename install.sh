#!/usr/bin/env bash

root=$(pwd)

install_cmd() {
    echo "Installing cmd..."
    
    cd $root/cmd/tie
    go install

    cd $root/cmd/tie-daemon
    go install

    cd $root/cmd/tie-download
    go install

    cd $root/cmd/tie-upload
    go install

    cd $root/cmd/tie-upload
    go install

    cd $root/cmd/tie-serve
    go install
    
    cd $root
}

install_gui() {
    echo "Installing gui..."
    
    cd $root/gui/tie-tag
    go install

}

case "$1" in
"cmd")
    $root/build.sh cmd
    install_cmd
    ;;

"gui")
    $root/build.sh cmd
    install_gui
    ;;
    
"")
    $root/build.sh
    install_cmd
    install_gui
    
esac
