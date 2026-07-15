#!/usr/bin/env sh

root=$(pwd)

install_cmd() {
    echo "Installing cmd..."
    
    cd $root/cmd/tie
    go install

    cd $root/cmd/tie-daemon
    go install

    cd $root/cmd/tie-handle
    go install

    cd $root/cmd/tie-filehost
    go install
    
    cd $root
}

install_cmd
