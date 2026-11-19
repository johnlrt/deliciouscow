#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

usage() {
  cat <<'EOF'
Usage: ./scripts/cordova-dev.sh <command> [options]

Commands:
  up              Prepare local dev config/certs, start dev stack, enroll & start mock-node
  status          Show status of dev Compose services (including mock-node)
  logs [service]  Follow logs (optionally for a specific service, e.g. server or mock-node)
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

dump_diagnostics() {
  echo "=== CORDOVA DEV FAILURE DIAGNOSTICS ===" >&2
  compose_cmd --profile mock-node ps -a >&2 || true
  local cids
  cids="$(compose_cmd --profile mock-node ps -aq 2>/dev/null || true)"
  if [ -n "$cids" ]; then
    echo "--- Container states, exit codes, and healthchecks ---" >&2
    for cid in $cids; do
      docker inspect --format '{{.Name}} status={{.State.Status}} exit_code={{.State.ExitCode}} health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$cid" >&2 || true
    done
    echo "--- Container logs ---" >&2
    compose_cmd --profile mock-node logs --no-color --tail=200 >&2 || true
  fi
  echo "=======================================" >&2
}

provision_demo_and_mock_node() {
  local base_url="https://127.0.0.1:8443"
  local curl_bin="curl"
  if ! command -v curl >/dev/null 2>&1; then
    curl_bin="docker run --rm --network host curlimages/curl:8.12.1"
  fi

  local tmp_dir
  tmp_dir="$(mktemp -d)"
  local cookie="$tmp_dir/cookies.txt"
  local demo_user="devdemo"
  local demo_pass="correcthorsebatterystaple"

  echo "Provisioning local demo account and enrolling mock-node..."
  compose_cmd --profile mock-node build mock-node

  local auth_json=""
  auth_json="$($curl_bin -sk -c "$cookie" -X POST "$base_url/api/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"$demo_user\",\"password\":\"$demo_pass\"}" || true)"
  local csrf
  csrf="$(printf '%s' "$auth_json" | sed -n 's/.*"csrf_token":"\([^"]*\)".*/\1/p')"
  if [ -z "$csrf" ]; then
    auth_json="$($curl_bin -skf -c "$cookie" -X POST "$base_url/api/auth/register" \
      -H "Content-Type: application/json" \
      -d "{\"username\":\"$demo_user\",\"password\":\"$demo_pass\",\"privacy_notice_version\":\"v1\"}")"
    csrf="$(printf '%s' "$auth_json" | sed -n 's/.*"csrf_token":"\([^"]*\)".*/\1/p')"
  fi
  if [ -z "$csrf" ]; then
    rm -rf "$tmp_dir"
    echo "ERROR: failed to authenticate or register local demo account" >&2
    return 1
  fi

  $curl_bin -skf -b "$cookie" -H "X-CSRF-Token: $csrf" -X POST "$base_url/api/dev/deposit" \
    -H "Content-Type: application/json" \
    -d '{"amount_piconero":"10000000000"}' >/dev/null

  local key_json
  key_json="$($curl_bin -skf -b "$cookie" -H "X-CSRF-Token: $csrf" -X POST "$base_url/api/keys" \
    -H "Content-Type: application/json" \
    -d '{"name":"dev-up-key"}')"
  local api_key
  api_key="$(printf '%s' "$key_json" | sed -n 's/.*"secret":"\([^"]*\)".*/\1/p')"
  if [ -z "$api_key" ]; then
    rm -rf "$tmp_dir"
    echo "ERROR: failed to create demo API key" >&2
    return 1
  fi

  umask 077
  cat > .local/demo.env <<EOF
# Auto-generated local demo credentials for https://localhost:8443
DEMO_USERNAME=$demo_user
DEMO_PASSWORD=$demo_pass
DEMO_API_KEY=$api_key
EOF
  chmod 600 .local/demo.env

  local login_log="$tmp_dir/node-login.log"
  compose_cmd --profile mock-node run --rm -T --no-deps mock-node \
    login --server http://server:8080 --name dev-mock-node >"$login_log" 2>&1 &
  local login_pid=$!

  local user_code=""
  for _ in $(seq 1 60); do
    if grep -q "enter code:" "$login_log" 2>/dev/null; then
      user_code="$(sed -n 's/.*enter code: //p' "$login_log" | tr -d '[:space:]')"
      break
    fi
    sleep 0.5
  done
  if [ -z "$user_code" ]; then
    cat "$login_log" >&2 || true
    rm -rf "$tmp_dir"
    echo "ERROR: mock-node did not output enrollment user_code" >&2
    return 1
  fi

  $curl_bin -skf -b "$cookie" -H "X-CSRF-Token: $csrf" -X POST "$base_url/api/nodes/link" \
    -H "Content-Type: application/json" \
    -d "{\"user_code\":\"$user_code\",\"name\":\"dev-mock-node\"}" >/dev/null

  wait "$login_pid"
  rm -rf "$tmp_dir"

  compose_cmd --profile mock-node up -d --force-recreate --no-deps mock-node

  local ready=0
  for _ in $(seq 1 40); do
    if $curl_bin -skf "$base_url/v1/models" | grep -q '"availability":"available"'; then
      ready=1
      break
    fi
    sleep 0.5
  done
  if [ "$ready" -ne 1 ]; then
    echo "ERROR: model cordova-tiny-chat did not become available after mock-node started" >&2
    return 1
  fi

  echo ""
  echo "Cordova dev environment is ready (with connected mock-node):"
  echo "  Web UI:        https://localhost:8443  (self-signed dev TLS cert)"
  echo "  Demo login:    $demo_user / $demo_pass  (saved in .local/demo.env)"
  echo "  Demo API key:  $api_key"
  echo "  Model ready:   cordova-tiny-chat (availability: available)"
  echo ""
  echo "Test SSE completion right now:"
  echo "  curl -skN https://localhost:8443/v1/chat/completions \\"
  echo "    -H \"Authorization: Bearer $api_key\" \\"
  echo "    -H \"Content-Type: application/json\" \\"
  echo "    -d '{\"model\":\"cordova-tiny-chat\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}'"
  echo ""
  echo "Manage stack:"
  echo "  Status:   ./scripts/cordova-dev.sh status"
  echo "  Logs:     ./scripts/cordova-dev.sh logs [service]"
  echo "  Smoke:    ./scripts/cordova-dev.sh smoke"
  echo "  Stop:     ./scripts/cordova-dev.sh down"
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
    SERVICES="$(compose_cmd config --services)"
    for forbidden in monerod monero-wallet-rpc payments; do
      if echo "$SERVICES" | grep -qx "$forbidden"; then
        echo "ERROR: $forbidden must not be enabled in default dev Compose profile" >&2
        exit 1
      fi
    done
    echo "Building and starting Cordova dev stack (without Monero/payments)..."
    if ! compose_cmd up -d --build --wait; then
      dump_diagnostics
      exit 1
    fi
    if ! provision_demo_and_mock_node; then
      dump_diagnostics
      exit 1
    fi
    ;;
  status)
    require_docker
    require_files
    if [ ! -f .local/dev.env ]; then
      echo "Dev environment is not initialized yet (.local/dev.env missing). Run: ./scripts/cordova-dev.sh up"
      exit 0
    fi
    compose_cmd --profile mock-node ps
    ;;
  logs)
    require_docker
    require_files
    if [ ! -f .local/dev.env ]; then
      echo "ERROR: .local/dev.env missing. Run './scripts/cordova-dev.sh up' first." >&2
      exit 1
    fi
    compose_cmd --profile mock-node logs -f --tail=100 "$@"
    ;;
  smoke)
    require_docker
    require_files
    KEEP_UP="${SMOKE_KEEP_UP:-0}"
    if [ -f .local/dev.env ] && [ -n "$(compose_cmd ps -q nginx 2>/dev/null || true)" ]; then
      KEEP_UP=1
    fi
    SMOKE_KEEP_UP="$KEEP_UP" exec bash scripts/smoke-compose.sh
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
