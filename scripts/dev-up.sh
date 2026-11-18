#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if [ ! -d frontend/node_modules ]; then
	echo "Installing frontend dependencies..."
	(cd frontend && npm ci)
fi

echo "Building frontend..."
(cd frontend && npm run build)

echo "Building cordova-server..."
make -C server build

ADDR="${ADDR:-127.0.0.1:8443}"
METRICS_ADDR="${METRICS_ADDR:-127.0.0.1:9090}"
SNAP_PATH="${SNAP_PATH:-$ROOT_DIR/.local/state.json}"
mkdir -p "$(dirname "$SNAP_PATH")"

echo "Starting cordova-server on http://${ADDR} (metrics: http://${METRICS_ADDR}/metrics)"
exec env \
	ADDR="$ADDR" \
	METRICS_ADDR="$METRICS_ADDR" \
	SNAP_PATH="$SNAP_PATH" \
	STATIC_DIR="$ROOT_DIR/frontend/build" \
	DEV_MODE=1 \
	DEV_INSECURE_COOKIES=1 \
	"$ROOT_DIR/server/bin/cordova-server"
