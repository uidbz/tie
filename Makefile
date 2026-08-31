ROOT    := $(CURDIR)
DIST    := $(ROOT)/dist
CMDS    := tie tie-triplestore tie-filehost

# Version embedded in the binaries. Defaults to the current git description
# (nearest tag + commits-since), overridable with `make build VERSION=v0.4.0`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/uidbz/tie/version.Version=$(VERSION)

.PHONY: build install install-server tls-keys push release clean

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

# Update dependencies, commit everything, and push. Usage: make push MSG="message"
push:
	@test -n "$(MSG)" || { echo "Usage: make push MSG=\"commit message\""; exit 1; }
	go get -u .
	go mod tidy
	git add -A
	git commit -m "$(MSG)"
	git push

# Tag a release and push the tag. Usage: make release VERSION=v0.4.0
release:
	@test -n "$(VERSION)" || { echo "Usage: make release VERSION=<tag>  (e.g. v0.4.0)"; exit 1; }
	git tag $(VERSION)
	git push origin $(VERSION)

clean:
	rm -rf $(DIST)
