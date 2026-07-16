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

# Install all commands into GOBIN.
install:
	@for cmd in $(CMDS); do \
		echo "Installing $$cmd..."; \
		go install ./cmd/$$cmd || exit 1; \
	done

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

# Tag a release and push the tag. Usage: make release VERSION=3.7  (tags v0.3.7)
release:
	@test -n "$(VERSION)" || { echo "Usage: make release VERSION=<n>  (tags v0.<n>)"; exit 1; }
	git tag v0.$(VERSION)
	git push origin v0.$(VERSION)

clean:
	rm -rf $(DIST)
