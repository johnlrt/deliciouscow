#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'kill ${SRV_PID:-} ${NODE_PID:-} 2>/dev/null || true; rm -rf "$TMP"' EXIT

export ADDR="127.0.0.1:18443"
export SNAP_PATH="$TMP/state.json"
export STATIC_DIR="$ROOT/frontend/build"
export DEV_MODE=1
export DEV_INSECURE_COOKIES=1

"$ROOT/server/bin/cordova-server" >"$TMP/server.log" 2>&1 &
SRV_PID=$!

for _ in $(seq 1 30); do
  if curl -sf "http://$ADDR/health" >/dev/null; then break; fi
  sleep 0.2
done

# 1. Verify frontend static HTML is served
HTML=$(curl -sf "http://$ADDR/")
echo "$HTML" | grep -q "Cordova"

# 2. Register user, deposit, create API key
COOKIE="$TMP/cookies.txt"
REG=$(curl -sf -c "$COOKIE" -X POST "http://$ADDR/api/auth/register" \
  -d '{"username":"smokeuser","password":"correcthorsebatterystaple","privacy_notice_version":"v1"}')
CSRF=$(node -e "console.log(JSON.parse(process.argv[1]).csrf_token)" "$REG")

curl -sf -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X POST "http://$ADDR/api/dev/deposit" \
  -d '{"amount_piconero":"10000000000"}' >/dev/null

KEY_JSON=$(curl -sf -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X POST "http://$ADDR/api/keys" \
  -d '{"name":"smoke-key"}')
API_KEY=$(node -e "console.log(JSON.parse(process.argv[1]).secret)" "$KEY_JSON")

# 3. Enroll Rust cordova-node (start login in background, extract user_code, link via API)
export CORDOVA_NODE_CONFIG="$TMP/node-config.json"
"$ROOT/node/target/release/cordova-node" login --server "http://$ADDR" --name "smoke-node" >"$TMP/login.log" 2>&1 &
LOGIN_PID=$!

USER_CODE=""
for _ in $(seq 1 30); do
  if grep -q "enter code:" "$TMP/login.log" 2>/dev/null; then
    USER_CODE=$(sed -n 's/.*enter code: //p' "$TMP/login.log" | tr -d '[:space:]')
    break
  fi
  sleep 0.2
done
[ -n "$USER_CODE" ]

curl -sf -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X POST "http://$ADDR/api/nodes/link" \
  -d "{\"user_code\":\"$USER_CODE\",\"name\":\"smoke-node\"}" >/dev/null

wait "$LOGIN_PID"

# 4. Run Rust cordova-node
"$ROOT/node/target/release/cordova-node" run --backend mock --model cordova-tiny-chat >"$TMP/node.log" 2>&1 &
NODE_PID=$!

# Wait for model to become available
for _ in $(seq 1 30); do
  if curl -sf "http://$ADDR/v1/models" | grep -q '"availability":"available"'; then break; fi
  sleep 0.2
done

# 5. Call OpenAI /v1/chat/completions (non-stream + stream)
RESP=$(curl -sf -X POST "http://$ADDR/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"model":"cordova-tiny-chat","messages":[{"role":"user","content":"hello"}]}')
echo "$RESP" | grep -q "Hello from Cordova Rust node"

SSE=$(curl -sfN -X POST "http://$ADDR/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"model":"cordova-tiny-chat","stream":true,"messages":[{"role":"user","content":"stream"}]}')
echo "$SSE" | grep -q "data: \[DONE\]"

echo "PASS: M1 full-stack smoke (Go server + Rust cordova-node + Svelte static build)"
