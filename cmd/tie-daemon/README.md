# tie-daemon

https://tiedb.eth.link (soon available)

### Configuration

All settings — server options and user accounts — live in a TOML config file.
Copy `tie-daemon.toml.example` to `tie-daemon.toml` and edit it. Access
management is done by adding/removing `[[Users]]` entries; passwords are stored
in plaintext, so keep the file readable only by the daemon's user.

### Installation & run
```
git clone https://github.com/uidbz/tie
cd tie
go build ./cmd/tie-daemon
./tie-daemon -config tie-daemon.toml
```

### Run with Docker

The `Dockerfile` at the repo root builds a single image that runs both
tie-daemon and tie-filehost together:
```
docker build -t tie .
docker run -p 1161:1161 -p 1162:1162 -v tie-data:/data tie
```
See the root `README.md` and `contrib/` for running as system services
(systemd / OpenRC).

### Interaction
* For CLI use: [tie](https://github.com/uidbz/tie)
* For GUI use: [tie-gui](https://github.com/uidbz/tie-gui)
* For Golang pkg use: [tie-client](https://github.com/uidbz/tie-client)
