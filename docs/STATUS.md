# Status
Milestones: **M1 vertical slice + M2/infra adapters** implemented.

| Component | Path | State |
| --- | --- | --- |
| Go server | `server/` | Implemented (`MemoryStore` + `RedisStore` with Lua & `WAITAOF`, pure-Go `KafkaBus` C7 producer, `MockWallet` + `MoneroRPCWallet` C8, `SimpleEstimator` + `HelperClient` C9) |
| Rust node | `node/` | Implemented (`cordova-node` login/run/status/logout, `#![forbid(unsafe_code)]`, C3 WS, `MockBackend` + `LlamaServerBackend`) |
| Svelte dashboard | `frontend/` | Implemented (Svelte 5 + SvelteKit 2 static adapter, all `PLAN-front.md` routes, CSRF + typed API client + C4 live WS) |
| Infra | `infra/` | Implemented (`compose.yaml`, `nginx.conf`, `Dockerfile.server`, `Dockerfile.node`) |
