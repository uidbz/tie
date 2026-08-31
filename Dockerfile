# Combined image: runs both tie-triplestore and tie-filehost in one container.
#
# Build:  docker build -t tie .
# Run:    docker run -p 1161:1161 -p 1162:1162 -v tie-data:/data tie
#
# On first run, default configs are generated into /etc/tie pointing at the
# /data volume (triplestore db in /data/db, filehost blobs in /data/data).
# Override by mounting your own /etc/tie, or set TIE_USER / TIE_PASSWORD env vars
# to seed the triplestore's initial account. TLS is expected to be terminated by
# a reverse proxy in front of the container.
FROM golang:1.25-alpine AS builder
WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/tie-triplestore ./cmd/tie-triplestore && \
    CGO_ENABLED=0 GOOS=linux go build -o /out/tie-filehost ./cmd/tie-filehost

FROM alpine:latest
RUN apk add --no-cache bash ca-certificates

COPY --from=builder /out/tie-triplestore /usr/local/bin/tie-triplestore
COPY --from=builder /out/tie-filehost /usr/local/bin/tie-filehost
COPY contrib/docker/entrypoint.sh     /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh && mkdir -p /etc/tie /data

# tie-triplestore (triple store) and tie-filehost (blob store).
EXPOSE 1161 1162
VOLUME ["/data"]

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
