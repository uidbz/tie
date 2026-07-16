# tie-daemon

https://tiedb.eth.link (soon available)

### Configuration

All settings — server options and user accounts — live in a TOML config file.
Copy `tie-daemon.toml.example` to `tie-daemon.toml` and edit it. Access
management is done by adding/removing `[[Users]]` entries; passwords are stored
in plaintext, so keep the file readable only by the daemon's user.

### Installation & run
```
go get git.sr.ht/~uid/tie-daemon ...
cd $GOPATH/src/git.sr.ht/~uid/tie-daemon
go build
./tie-daemon -config tie-daemon.toml
```

### Run with Docker
```
docker run -p 1161:1161 -v <db-path>:/data -v <config-path>:/config uid3/tie-daemon:latest -config /config/tie-daemon.toml
```

### Interaction
* For CLI use: [tie](https://sr.ht/~uid/tie)
* For GUI use: [tie-gui](https://sr.ht/~uid/tie-gui)
* For Golang pkg use: [tie-client](https://sr.ht/~uid/tie-client)
