#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

usage() {
  cat <<'EOF'
Usage: ./scripts/cordova-dev.sh <command> [options]

Commands:
  up              Prepare local dev config/certs, validate Compose, build & start dev stack
  status          Show status of dev Compose services
  logs [service]  Follow logs (optionally for a specific service)
  smoke           Run end-to-end Compose dev smoke test (UI, auth, mock-node, SSE)
  down            Stop dev services (preserves persistent volumes and .local/ secrets)
  reset [--yes]   Stop services AND delete persistent volumes + .local/ (requires confirmation)
EOF
}

require_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    echo "ERROR: 'docker' CLI not found. Install Docker Engine / Docker Desktop first." >&2
    exit 1
  fi
  if ! docker compose version >/dev/null 2>&1; then
    echo "ERROR: Docker Compose v2 ('docker compose') is required." >&2
    exit 1
  fi
  if ! docker info >/dev/null 2>&1; then
    echo "ERROR: Docker daemon is not running or not reachable." >&2
    exit 1
  fi
}

require_files() {
  local f
  for f in infra/compose.yaml infra/compose.dev.yaml infra/nginx/nginx.conf infra/redis/redis.conf; do
    if [ ! -f "$f" ]; then
      echo "ERROR: required file missing: $f" >&2
      exit 1
    fi
  done
}

compose_cmd() {
  docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml "$@"
}

port_8443_in_use() {
  if command -v python3 >/dev/null 2>&1; then
    python3 -c '
import socket, sys
s = socket.socket()
s.settimeout(0.5)
try:
    s.connect(("127.0.0.1", 8443))
    sys.exit(0)
except OSError:
    sys.exit(1)
finally:
    s.close()
'
    return $?
  elif command -v ss >/dev/null 2>&1; then
    ss -ltn | awk '{print $4}' | grep -qE '(^|:)8443$'
    return $?
  elif command -v lsof >/dev/null 2>&1; then
    lsof -iTCP:8443 -sTCP:LISTEN -Pn >/dev/null 2>&1
    return $?
  fi
  return 1
}

check_port_available() {
  local nginx_cid
  nginx_cid="$(compose_cmd ps -q nginx 2>/dev/null || true)"
  if [ -n "$nginx_cid" ]; then
    return 0
  fi
  if port_8443_in_use; then
    echo "ERROR: port 8443 on 127.0.0.1 is already in use by another process." >&2
    echo "Stop the conflicting service or free port 8443 before starting Cordova." >&2
    exit 1
  fi
}

CMD="${1:-}"
if [ -z "$CMD" ]; then
  usage
  exit 1
fi
shift || true

case "$CMD" in
  up)
    require_docker
    require_files
    bash scripts/bootstrap-dev.sh
    check_port_available
    echo "Validating Compose configuration..."
    compose_cmd config -q
    echo "Building and starting Cordova dev stack (without Monero/payments)..."
    compose_cmd up -d --build --wait
    echo ""
    echo "Cordova dev environment is ready:"
    echo "  Web UI:   https://localhost:8443  (self-signed dev TLS cert)"
    echo "  Status:   ./scripts/cordova-dev.sh status"
    echo "  Logs:     ./scripts/cordova-dev.sh logs"
    echo "  Smoke:    ./scripts/cordova-dev.sh smoke"
    echo "  Stop:     ./scripts/cordova-dev.sh down"
    ;;
  status)
    require_docker
    require_files
    if [ ! -f .local/dev.env ]; then
      echo "Dev environment is not initialized yet (.local/dev.env missing). Run: ./scripts/cordova-dev.sh up"
      exit 0
    fi
    compose_cmd ps
    ;;
  logs)
    require_docker
    require_files
    if [ ! -f .local/dev.env ]; then
      echo "ERROR: .local/dev.env missing. Run './scripts/cordova-dev.sh up' first." >&2
      exit 1
    fi
    compose_cmd logs -f --tail=100 "$@"
    ;;
  smoke)
    exec bash scripts/smoke-compose.sh
    ;;
  down)
    require_docker
    require_files
    if [ ! -f .local/dev.env ]; then
      echo "Nothing to stop (.local/dev.env missing)."
      exit 0
    fi
    echo "Stopping Cordova dev services (preserving persistent volumes and .local/)..."
    compose_cmd --profile mock-node down
    ;;
  reset)
    require_docker
    require_files
    YES=0
    if [ "${1:-}" = "--yes" ]; then
      YES=1
    fi
    if [ "$YES" -ne 1 ]; then
      echo "WARNING: This will stop all Cordova dev containers and permanently delete Docker volumes and .local/ secrets/certs."
      printf "Type DELETE to confirm: "
      read -r CONFIRM
      if [ "$CONFIRM" != "DELETE" ]; then
        echo "Aborted."
        exit 1
      fi
    fi
    if [ -f .local/dev.env ]; then
      compose_cmd --profile mock-node --profile payments --profile ops down -v --remove-orphans || true
    fi
    rm -rf .local
    echo "Cordova dev volumes and .local/ removed."
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    echo "ERROR: unknown command '$CMD'" >&2
    usage
    exit 1
    ;;
esac
