#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

for cmd in docker; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "ERROR: $cmd is required for Compose smoke" >&2
    exit 1
  fi
done
if ! docker compose version >/dev/null 2>&1; then
  echo "ERROR: Docker Compose v2 (docker compose) is required" >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "ERROR: Docker daemon is not running or not reachable" >&2
  exit 1
fi

bash scripts/bootstrap-dev.sh

COMPOSE=(docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml)

echo "[1/5] Validating Compose configuration..."
"${COMPOSE[@]}" config -q
SERVICES="$("${COMPOSE[@]}" config --services)"
for forbidden in monerod monero-wallet-rpc payments; do
  if echo "$SERVICES" | grep -qx "$forbidden"; then
    echo "FAIL: $forbidden must not be enabled in default dev Compose profile" >&2
    exit 1
  fi
done

TMP_DIR="$(mktemp -d)"
MOCK_CONTAINER=""
cleanup() {
  if [ -n "$MOCK_CONTAINER" ]; then
    docker rm -f "$MOCK_CONTAINER" >/dev/null 2>&1 || true
  fi
  echo "[5/5] Stopping dev Compose services (preserving persistent volumes)..."
  "${COMPOSE[@]}" --profile mock-node down >/dev/null 2>&1 || true
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

echo "[2/5] Building and starting dev Compose stack..."
"${COMPOSE[@]}" up -d --build --wait

BASE_URL="https://127.0.0.1:8443"
CURL_BIN="curl"
if ! command -v curl >/dev/null 2>&1; then
  CURL_BIN="docker run --rm --network host curlimages/curl:8.12.1"
fi

echo "[3/5] Checking web UI and auth/billing flow via nginx ($BASE_URL)..."
HTML="$($CURL_BIN -skf "$BASE_URL/")"
echo "$HTML" | grep -q "Cordova"

COOKIE="$TMP_DIR/cookies.txt"
UNAME="smoke_$(date +%s)"
REG="$($CURL_BIN -skf -c "$COOKIE" -X POST "$BASE_URL/api/auth/register" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"$UNAME\",\"password\":\"correcthorsebatterystaple\",\"privacy_notice_version\":\"v1\"}")"

CSRF="$(printf '%s' "$REG" | sed -n 's/.*"csrf_token":"\([^"]*\)".*/\1/p')"
if [ -z "$CSRF" ]; then
  echo "FAIL: failed to extract csrf_token from register response" >&2
  exit 1
fi

$CURL_BIN -skf -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X POST "$BASE_URL/api/dev/deposit" \
  -H "Content-Type: application/json" \
  -d '{"amount_piconero":"10000000000"}' >/dev/null

KEY_JSON="$($CURL_BIN -skf -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X POST "$BASE_URL/api/keys" \
  -H "Content-Type: application/json" \
  -d '{"name":"compose-smoke-key"}')"
API_KEY="$(printf '%s' "$KEY_JSON" | sed -n 's/.*"secret":"\([^"]*\)".*/\1/p')"
if [ -z "$API_KEY" ]; then
  echo "FAIL: failed to extract API key secret" >&2
  exit 1
fi

echo "[4/5] Enrolling mock-node container and testing SSE completion..."
"${COMPOSE[@]}" --profile mock-node build mock-node >/dev/null

# Start login in background container and capture user_code
LOGIN_LOG="$TMP_DIR/node-login.log"
"${COMPOSE[@]}" --profile mock-node run --rm -T --no-deps mock-node \
  login --server http://server:8080 --name compose-mock-node >"$LOGIN_LOG" 2>&1 &
LOGIN_PID=$!

USER_CODE=""
for _ in $(seq 1 60); do
  if grep -q "enter code:" "$LOGIN_LOG" 2>/dev/null; then
    USER_CODE="$(sed -n 's/.*enter code: //p' "$LOGIN_LOG" | tr -d '[:space:]')"
    break
  fi
  sleep 0.5
done
if [ -z "$USER_CODE" ]; then
  cat "$LOGIN_LOG" >&2 || true
  echo "FAIL: mock-node did not output enrollment user_code" >&2
  exit 1
fi

$CURL_BIN -skf -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X POST "$BASE_URL/api/nodes/link" \
  -H "Content-Type: application/json" \
  -d "{\"user_code\":\"$USER_CODE\",\"name\":\"compose-mock-node\"}" >/dev/null

wait "$LOGIN_PID"

MOCK_CONTAINER="$("${COMPOSE[@]}" --profile mock-node run -d --no-deps mock-node \
  run --backend mock --model cordova-tiny-chat)"

MODEL_READY=0
for _ in $(seq 1 40); do
  if $CURL_BIN -skf "$BASE_URL/v1/models" | grep -q '"availability":"available"'; then
    MODEL_READY=1
    break
  fi
  sleep 0.5
done
if [ "$MODEL_READY" -ne 1 ]; then
  echo "FAIL: model did not become available after mock-node started" >&2
  exit 1
fi

SSE="$($CURL_BIN -skfN -X POST "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"cordova-tiny-chat","stream":true,"messages":[{"role":"user","content":"hello compose"}]}')"

echo "$SSE" | grep -q "Hello from Cordova Rust node"
echo "$SSE" | grep -q "data: \[DONE\]"

echo "PASS: Compose dev smoke (nginx TLS + server + redis + kafka + tokenizer + mock-node + SSE)"
