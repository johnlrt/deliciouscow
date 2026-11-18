# Status
Milestones: **In progress (M1 vertical slice + partial M2/M3/infra adapters; milestone gates M0–M6 not yet signed off)**.

> Code presence alone does not pass a milestone gate (`docs/plan/PLAN.md`).

| Component | Path | State & Verified Evidence |
| --- | --- | --- |
| Go server & workers | `server/` | `MemoryStore` + `RedisStore` (`WATCH`/`MULTI`/`EXEC` for metadata; pure decimal-string Lua scripts without `tonumber` + dedicated-connection `WAITAOF` for financial operations in `internal/ledger/`), `KafkaBus` C7 producer, `MockWallet` + `MoneroRPCWallet` C8, `SimpleEstimator` + `HelperClient` C9. **Verified:** `go vet ./...` and `go test -race ./...` (unit, concurrency, `>2^53` precision, overflow, and recovery tests against `MemoryStore` + static Lua script guards). **Unverified in this environment (no `redis-server`/Docker):** `REDIS_ADDR=... go test -race ./internal/ledger/...` (real Redis 7.2+ Lua execution, `WAITAOF`, and multi-process `WATCH` contention). |
| Rust node | `node/` | Implemented (`cordova-node` login/run/status/logout, `#![forbid(unsafe_code)]`, C3 WS, `MockBackend` + `LlamaServerBackend`). |
| Svelte dashboard | `frontend/` | Implemented (Svelte 5 + SvelteKit 2 static adapter, all `PLAN-front.md` routes, CSRF + typed API client + C4 live WS). |
| Infra | `infra/` | Implemented (`compose.yaml`, `nginx.conf`, `Dockerfile.server`, `Dockerfile.node`). |
