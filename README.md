# deliciouscow — Cordova v1 (M1 vertical slice)

Сеть сообщества для запуска LLM с OpenAI-совместимым API, расчётами в piconero (XMR), Go-координатором, Rust-нодой и дашбордом на Svelte 5.

## Состав репозитория
- [`server/`](server/README.md) — Go-сервер (`cordova-server`): аутентификация (scrypt, TOTP, recovery-коды, CSRF, `__Host-cordova_session`), API-ключи (`cvk_`), device-code enrollment и Ed25519 JWT для нод, C3 Node WebSocket v1 (`/api/node/ws`), C4 Dashboard Live WebSocket (`/api/live`), OpenAI `/v1/models` и `/v1/chat/completions` (обычный + SSE), целочисленный piconero ledger (C5), `MemoryStore` с атомарным снимком (`RedisStore` заглушен под M2), `MockWallet` (C8).
- [`node/`](node/README.md) — Rust-нода (`cordova-node`, `#![forbid(unsafe_code)]`): команды `login`, `run`, `status`, `logout`, C3 WebSocket, `MockBackend` и `LlamaServerBackend`.
- [`frontend/`](frontend/README.md) — Svelte 5 + SvelteKit 2 (`adapter-static`) + TypeScript strict: все маршруты из `docs/plan/PLAN-front.md` (`/`, `/login`, `/register`, `/recover`, `/dashboard/*`, `/docs/*`), статическая сборка в `frontend/build/` (раздаётся сервером).
- [`scripts/smoke-m1.sh`](scripts/smoke-m1.sh) — сквозная проверка связки Go server + Rust `cordova-node` + Svelte build.
- [`mini/`](mini/README.md) — ранний упрощённый прототип на Node.js.
- [`docs/plan/`](docs/plan/PLAN.md) и [`contracts/`](contracts/openapi.yaml) — спецификация и контракты.

## Проверка и запуск
```sh
make test    # тесты Go (-race), Rust (fmt + clippy + test), Svelte (svelte-check + vitest)
make build   # сборка server/bin/cordova-server, node/target/release/cordova-node, frontend/build
make smoke   # сквозной E2E-прогон всех трёх компонентов вместе
```
Для локального запуска с веб-интерфейсом:
```sh
cd server && ./bin/cordova-server
# http://127.0.0.1:8443/
```
