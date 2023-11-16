#!/usr/bin/env sh 

# SERVER
# For tie-daemon and tie-fileserver generate self-signed certificate with below commond.
# Update hostname to real hostname
# Use .key & .crt for the servers

# CLIENT
# Copy .crt (from server) to client and update to make it trusted
# cp localhost.crt /usr/local/share/ca-certificates/localhost.crt
# update-ca-certificates

openssl req -newkey rsa:2048 -nodes -keyout localhost.key -x509 -days 3650 -out localhost.crt -subj "/CN=localhost" -addext "subjectAltName = DNS:localhost"
