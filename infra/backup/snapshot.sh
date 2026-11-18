#!/usr/bin/env bash
set -euo pipefail
OUT_DIR="${1:-.local/backups}"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$OUT_DIR"
SRC="${SNAP_PATH:-.local/state.json}"
if [ -f "$SRC" ]; then
  cp "$SRC" "$OUT_DIR/state-$TS.json"
  sha256sum "$OUT_DIR/state-$TS.json" > "$OUT_DIR/state-$TS.json.sha256"
  echo "Snapshot saved: $OUT_DIR/state-$TS.json"
else
  echo "No snapshot file at $SRC" >&2
  exit 1
fi
