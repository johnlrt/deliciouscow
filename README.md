# deliciouscow — Cordova v1 (M1 vertical slice)

Сеть сообщества для запуска LLM с OpenAI-совместимым API, расчётами в piconero (XMR), Go-координатором, Rust-нодой и дашбордом на Svelte 5.

## Состав репозитория
- [`server/`](server/README.md) — Go-сервер (`cordova-server`): аутентификация (scrypt, TOTP, recovery-коды, CSRF, `__Host-cordova_session`), API-ключи (`cvk_`), device-code enrollment и Ed25519 JWT для нод, C3 Node WebSocket v1 (`/api/node/ws`), C4 Dashboard Live WebSocket (`/api/live`), OpenAI `/v1/models` и `/v1/chat/completions` (обычный + SSE), целочисленный piconero ledger (C5), `MemoryStore` с атомарным снимком (`RedisStore` заглушен под M2), `MockWallet` (C8).
- [`node/`](node/README.md) — Rust-нода (`cordova-node`, `#![forbid(unsafe_code)]`): команды `login`, `run`, `status`, `logout`, C3 WebSocket, `MockBackend` и `LlamaServerBackend`.
- [`frontend/`](frontend/README.md) — Svelte 5 + SvelteKit 2 (`adapter-static`) + TypeScript strict: все маршруты из `docs/plan/PLAN-front.md` (`/`, `/login`, `/register`, `/recover`, `/dashboard/*`, `/docs/*`), статическая сборка в `frontend/build/` (раздаётся сервером).
- [`scripts/smoke-m1.sh`](scripts/smoke-m1.sh) — сквозная проверка связки Go server + Rust `cordova-node` + Svelte build.
- [`mini/`](mini/README.md) — ранний упрощённый прототип на Node.js.
- [`docs/plan/`](docs/plan/PLAN.md) и [`contracts/`](contracts/openapi.yaml) — спецификация и контракты.

## Быстрый запуск локальной демо-версии (Docker)

Сборка Go-сервера, Rust-ноды и frontend выполняется внутри контейнеров.

### Требования к окружению
- **Linux / macOS:** Docker Engine (или Docker Desktop) + Docker Compose v2 (`docker compose`), `bash`.
- **Windows:** Docker Desktop (включая Docker Compose v2) + PowerShell 5.1 / PowerShell 7+ (для `smoke` также используется встроенный в Windows 10/11 `curl.exe`).
- Свободный порт `8443` на `127.0.0.1`.

> По умолчанию dev-профиль запускает `nginx` (HTTPS на `https://localhost:8443`), `server`, `tokenizer`, `ranker`, `event-publisher`, `projections`, `redis`, `kafka` и контейнер `mock-node` (`dev-mock-node`). Сервисы `monerod`, `monero-wallet-rpc` и `payments` **не запускаются** (`profiles: ["payments"]`), реальные кошельки и mainnet не используются.
> При первом запуске автоматически создаются локальные dev-секреты (`.local/dev.env`), self-signed сертификат (`.local/certs/server.{crt,key}`), демо-аккаунт (`devdemo` / `correcthorsebatterystaple` с тестовым балансом и API-ключом в `.local/demo.env`), а также подключается и запускается `mock-node` (модель `cordova-tiny-chat` сразу переходит в `available`).

### Linux / macOS
```sh
./scripts/cordova-dev.sh up       # подготовка .local/, запуск dev-стека, подключение mock-node и вывод готовой команды SSE (или: make dev-up)
./scripts/cordova-dev.sh status   # статус сервисов, включая mock-node (или: make dev-status)
./scripts/cordova-dev.sh logs     # просмотр логов (или: make dev-logs)
./scripts/cordova-dev.sh smoke    # сквозная проверка: UI, регистрация, dev-депозит, API-ключ, mock-node, SSE (или: make dev-smoke)
./scripts/cordova-dev.sh down     # остановка без удаления persistent volumes и .local/ (или: make dev-down)
./scripts/cordova-dev.sh reset    # полная очистка volumes и .local/ (требует подтверждения DELETE; или: make dev-reset)
```

### Windows (PowerShell)
```powershell
.\scripts\cordova-dev.ps1 up
.\scripts\cordova-dev.ps1 status
.\scripts\cordova-dev.ps1 logs
.\scripts\cordova-dev.ps1 smoke
.\scripts\cordova-dev.ps1 down
.\scripts\cordova-dev.ps1 reset
```

После выполнения `up`:
1. Откройте в браузере `https://localhost:8443` (подтвердите self-signed dev-сертификат) — можно войти под демо-аккаунтом `devdemo` / `correcthorsebatterystaple` (или зарегистрировать новый аккаунт) и отправить запрос в `/dashboard/playground`.
2. Либо выполните выведенную `up` команду `curl` / `curl.exe` к `https://127.0.0.1:8443/v1/chat/completions` с ключом `DEMO_API_KEY` из `.local/demo.env` для получения потокового SSE-ответа (`data: [DONE]`).

## Локальная проверка и сборка без Docker
```sh
make config-check  # проверка compose-конфигурации, профилей и nginx SSE
make test          # тесты Go (-race), Rust (fmt + clippy + test), Svelte (svelte-check + vitest)
make build         # сборка server/bin/cordova-server, node/target/release/cordova-node, frontend/build
make smoke         # сквозной E2E-прогон на хосте (без Docker)
```
