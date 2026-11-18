# Status
Milestone: **M1 vertical slice** — implemented and verified end-to-end.

| Component | Path | State |
| --- | --- | --- |
| Go server | `server/` | Implemented (auth, TOTP, recovery, API keys, enrollment, C3 WS, C4 live WS, OpenAI chat + SSE, C5 integer piconero ledger, MemoryStore + atomic snapshot, mock wallet) |
| Rust node | `node/` | Implemented (`cordova-node` login/run/status/logout, `#![forbid(unsafe_code)]`, C3 WS, MockBackend + LlamaServerBackend) |
| Svelte dashboard | `frontend/` | Implemented (Svelte 5 + SvelteKit 2 static adapter, all `PLAN-front.md` routes, CSRF + typed API client + C4 live WS) |

Deferred for M2/infra (requires Docker, Redis 7.2+, Kafka, stagenet `monero-wallet-rpc`, `llama.cpp` binary):
- Redis + Lua + `WAITAOF` adapter (`server/internal/store/redis.go` stubbed)
- Kafka ranking/event bus
- Real Monero wallet RPC & stagenet deposits/payouts
- Trusted `llama.cpp` tokenizer helper
