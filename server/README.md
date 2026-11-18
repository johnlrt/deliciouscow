# Cordova Go Server (`server/`)

M1 vertical slice implementing the Cordova shared contracts (`docs/plan/PLAN.md`):
- Auth: register (with 8 recovery codes), login, logout, recover, TOTP setup/confirm, step-up tokens, `__Host-cordova_session` (or `cordova_session` in HTTP dev mode) + `X-CSRF-Token`.
- API keys (`cvk_...`) with HMAC-SHA-256 peppered verifier, permissions, model scopes, and daily spend cap.
- Node enrollment (device-code flow), challenge/proof Ed25519 JWT auth, and C3 Node WebSocket v1 (`/api/node/ws`).
- OpenAI API: `GET /v1/models`, `POST /v1/chat/completions` (streaming SSE + non-streaming, reservation/settlement/refund ledger in integer piconero, retry on failed attempt, client cancellation, `Idempotency-Key`).
- Dashboard live WS (`/api/live` per C4), billing/deposit/payout endpoints (mock wallet C8 + `POST /api/dev/deposit` when `DEV_MODE=1`), usage & analytics.
- Persistence behind `store.Store` (`MemoryStore` with atomic JSON snapshot; `RedisStore` stubbed for M2/infra).

## Run & Test
```sh
make -C server test
make -C server run
```
