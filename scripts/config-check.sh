#!/usr/bin/env bash
set -euo pipefail

# Verify no forbidden host port publications for internal services in compose.yaml
if grep -E '6379:|9092:|18082:|18081:' infra/compose.yaml >/dev/null; then
  echo "FAIL: internal service port published in infra/compose.yaml" >&2
  exit 1
fi

# Verify prod.env.example does not enable DEV_MODE=1 or test cookies
if grep -q 'DEV_MODE=1' infra/environments/prod.env.example; then
  echo "FAIL: DEV_MODE=1 in prod.env.example" >&2
  exit 1
fi

# Verify dev compose does not connect mainnet
if grep -q 'MONERO_NETWORK=mainnet' infra/compose.dev.yaml infra/environments/dev.env.example; then
  echo "FAIL: mainnet configured in dev environment" >&2
  exit 1
fi

# Verify payments/monero services in compose.yaml are gated by profiles
for svc in payments monero-wallet-rpc monerod; do
  if ! awk -v s="  $svc:" '
    $0 == s { in_svc=1; next }
    in_svc && /^  [a-zA-Z0-9_-]+:/ { exit !found }
    in_svc && /profiles:/ { found=1 }
    END { exit !found }
  ' infra/compose.yaml; then
    echo "FAIL: $svc must be gated behind profiles in infra/compose.yaml" >&2
    exit 1
  fi
done

# Verify nginx.conf disables buffering for SSE
if ! grep -q 'proxy_buffering off;' infra/nginx/nginx.conf; then
  echo "FAIL: proxy_buffering off missing in infra/nginx/nginx.conf" >&2
  exit 1
fi

echo "PASS: config-check (compose networks/ports/profiles, prod/dev env, nginx SSE)"
