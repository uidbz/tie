# tie-daemon

https://tiedb.eth.link (soon available)

### Installation & run
```
go get git.sr.ht/~uid/tie-daemon ...
cd $GOPATH/src/git.sr.ht/~uid/tie-daemon
go build
./tie-daemon --db-path <path> [--tls-cert <file.crt> --tls-key <file.key> || --insecure]
```

### Run with Docker
```
docker run -p 1161:1161 -v <db-path>:/data -v <certificate-path>:/keys uid3/tie-daemon:latest --db-path /data --tls-cert /keys/<my-cert>.crt --tls-key /keys/<my-key>.key
```

### Interaction
* For CLI use: [tie](https://sr.ht/~uid/tie)
* For GUI use: [tie-gui](https://sr.ht/~uid/tie-gui)
* For Golang pkg use: [tie-client](https://sr.ht/~uid/tie-client)
