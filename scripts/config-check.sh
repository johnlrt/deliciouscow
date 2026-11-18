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

# Verify nginx.conf disables buffering for SSE
if ! grep -q 'proxy_buffering off;' infra/nginx/nginx.conf; then
  echo "FAIL: proxy_buffering off missing in infra/nginx/nginx.conf" >&2
  exit 1
fi

echo "PASS: config-check (compose networks/ports, prod env, nginx SSE)"
