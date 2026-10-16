ROOT    := $(CURDIR)
DIST    := $(ROOT)/dist
CMDS    := tie tie-triplestore tie-filehost tie-smb

# Version embedded in the binaries. Defaults to the current git description
# (nearest tag + commits-since), overridable with `make build VERSION=v0.4.0`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/uidbz/tie/version.Version=$(VERSION)

.PHONY: build install install-server tls-keys check vet staticcheck govulncheck test-race push release clean

# Build all commands into dist/, embedding the version via ldflags.
build:
	@echo "Build directory: $(DIST) (version $(VERSION))"
	@mkdir -p $(DIST)
	@for cmd in $(CMDS); do \
		echo "Building $$cmd..."; \
		go build -ldflags "$(LDFLAGS)" -o $(DIST)/ ./cmd/$$cmd || exit 1; \
	done

# Install just the tie client into GOBIN (or $GOPATH/bin). Rootless: this is
# the common case, since most machines only need the client. For the server
# components use `make install-server`.
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/tie

# Install the full stack — client, triplestore, and filehost — plus system services
# (systemd/OpenRC). Requires root, so run as `sudo make install-server`.
# See contrib/install.sh and contrib/README.md.
install-server:
	./contrib/install.sh

# Generate a self-signed localhost certificate for tie-triplestore / tie-filehost.
# Copy localhost.crt to clients and trust it there.
tls-keys:
	openssl req -newkey rsa:2048 -nodes -keyout localhost.key -x509 -days 3650 \
		-out localhost.crt -subj "/CN=localhost" -addext "subjectAltName = DNS:localhost"

# Static analysis and tests. Run this before pushing; CI runs the same set.
# staticcheck and govulncheck are pinned as module tools (go.mod tool
# directive), so `go tool` finds them in the module's tool cache.
# staticcheck.conf disables ST1005 (capitalized error strings) until the
# CLI error-message cleanup (issue #68) lands.
check: vet staticcheck govulncheck test-race

vet:
	go vet ./...

staticcheck:
	go tool staticcheck ./...
govulncheck:
	go tool govulncheck ./...

test-race:
	go test -race ./...

# Commit the tracked changes and push. Usage: make push MSG="message"
# (no `go get -u`: it silently upgrades every dependency at commit time;
# run it deliberately yourself. No `git add -A`: it sweeps untracked junk.)
push:
	@test -n "$(MSG)" || { echo "Usage: make push MSG=\"commit message\""; exit 1; }
	go mod tidy
	git commit -am "$(MSG)"
	git push

# Tag a release and push the tag. Usage: make release VERSION=v0.4.0
release:
	@test -n "$(VERSION)" || { echo "Usage: make release VERSION=<tag>  (e.g. v0.4.0)"; exit 1; }
	git tag $(VERSION)
	git push origin $(VERSION)

clean:
	rm -rf $(DIST)
