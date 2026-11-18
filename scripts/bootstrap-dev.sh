#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

mkdir -p .local/certs

rand_hex32() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c 'import secrets; print(secrets.token_hex(32))'
  elif command -v docker >/dev/null 2>&1; then
    docker run --rm alpine:3.21 sh -c 'head -c 32 /dev/urandom | od -An -tx1 | tr -d " \n"'
  else
    echo "ERROR: need openssl, python3, or docker to generate dev secrets" >&2
    exit 1
  fi
}

if [ ! -f .local/certs/server.crt ] || [ ! -f .local/certs/server.key ]; then
  if command -v openssl >/dev/null 2>&1; then
    openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
      -keyout .local/certs/server.key \
      -out .local/certs/server.crt \
      -subj "/CN=localhost" \
      -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
  elif command -v docker >/dev/null 2>&1; then
    docker run --rm -v "$ROOT_DIR/.local/certs:/certs" alpine/openssl \
      req -x509 -newkey rsa:2048 -nodes -days 365 \
      -keyout /certs/server.key \
      -out /certs/server.crt \
      -subj "/CN=localhost" \
      -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
  else
    echo "ERROR: need openssl or docker to generate self-signed dev certs" >&2
    exit 1
  fi
  chmod 600 .local/certs/server.key
  chmod 644 .local/certs/server.crt
  echo "Generated self-signed dev certs in .local/certs/"
else
  echo "Keeping existing dev certs in .local/certs/"
fi

if [ ! -f .local/dev.env ]; then
  umask 077
  cat > .local/dev.env <<EOF
# Auto-generated local development configuration (never used in production).
MONERO_NETWORK=stagenet
DEV_MODE=1
DEV_INSECURE_COOKIES=0
TOKENIZER_SECRET=$(rand_hex32)
API_KEY_PEPPER=$(rand_hex32)
NODE_SECRET_MASTER_KEY=$(rand_hex32)
EOF
  chmod 600 .local/dev.env
  echo "Generated local dev secrets in .local/dev.env"
else
  echo "Keeping existing .local/dev.env"
fi
