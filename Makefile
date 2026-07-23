ROOT    := $(CURDIR)
DIST    := $(ROOT)/dist
CMDS    := tie tie-daemon tie-filehost

.PHONY: build install tls-keys push release clean

# Build all commands into dist/.
build:
	@echo "Build directory: $(DIST)"
	@mkdir -p $(DIST)
	@for cmd in $(CMDS); do \
		echo "Building $$cmd..."; \
		go build -o $(DIST)/ ./cmd/$$cmd || exit 1; \
	done

# Install tie as system services (systemd/OpenRC). Requires root, so run as
# `sudo make install`. See contrib/install.sh and contrib/README.md.
install:
	./contrib/install.sh

# Generate a self-signed localhost certificate for tie-daemon / tie-filehost.
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
