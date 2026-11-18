#!/usr/bin/env bash
set -euo pipefail
mkdir -p .local/certs
if [ ! -f .local/certs/server.crt ]; then
  openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
    -keyout .local/certs/server.key \
    -out .local/certs/server.crt \
    -subj "/CN=localhost" \
    -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
  chmod 600 .local/certs/server.key
  echo "Generated self-signed dev certs in .local/certs/"
fi
