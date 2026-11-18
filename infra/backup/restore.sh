#!/usr/bin/env bash
set -euo pipefail
SNAP_FILE="${1:?usage: restore.sh <snapshot-json>}"
DEST="${SNAP_PATH:-.local/state.json}"
if [ -f "${SNAP_FILE}.sha256" ]; then
  sha256sum -c "${SNAP_FILE}.sha256"
fi
mkdir -p "$(dirname "$DEST")"
cp "$SNAP_FILE" "${DEST}.tmp"
mv "${DEST}.tmp" "$DEST"
echo "Restored $SNAP_FILE -> $DEST"
