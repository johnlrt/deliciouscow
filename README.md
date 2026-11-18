# Mini-Cordova

Примитивная «сырая» версия [Cordova](https://github.com/johnlrt/cordova): сеть сообщества для LLM-инференса с OpenAI-совместимым API. Два файла, ноль зависимостей (Node 18+).

| Cordova | Mini-Cordova |
| --- | --- |
| Go + Rust + Svelte, Redis, Kafka, nginx | `server.js` + `node.js`, состояние в `db.json` |
| XMR-депозиты и выплаты | фейковый `/api/topup`, выплата — заглушка |
| WebSocket-ноды, TLS, challenge-аутентификация | HTTP long-poll, секрет ноды (хранится хешем) |
| Точная токенизация llama.cpp | оценка токенов: символы / 4 |
| TOTP, CSRF, recovery-коды | логин/пароль (scrypt), Bearer-сессия |
| Ранжирование, ledger, reconcile | резерв → списание → возврат остатка, 80% владельцу ноды |

Оставлено: регистрация, API-ключи (`cvk_`), модели, `POST /v1/chat/completions` (обычный и SSE), `GET /v1/models`, ноды владельцев, баланс/заработок, usage, ошибки в формате OpenAI (`insufficient_funds`, `no_capacity`, `unsupported_parameter`).

Не сделано: платежи, шифрование, tools, масштабирование (всё в памяти одного процесса), защита от недобросовестных нод.

## Фронтенд
Встроенный веб-интерфейс на `http://localhost:8080/` (папка `public/`, чистый JS): регистрация/вход, обзор, ноды, API-ключи, баланс/выплата (заглушки), аналитика, документация.

## Запуск
```sh
node server.js                       # :8080, MODELS=tiny-chat
./test.sh                            # сквозной тест
```
1. `POST /api/register {username,password}` → `session`
2. `POST /api/topup {amount}`, `POST /api/keys` → `cvk_…`, `POST /api/nodes` → `node_secret` (Bearer session)
3. `SERVER=http://localhost:8080 NODE_SECRET=nsk_… node node.js` (mock) или с `BACKEND=http://localhost:11434/v1 BACKEND_MODEL=llama3` для реальной модели (ollama/llama-server)
4. `POST /v1/chat/completions` с `Authorization: Bearer cvk_…`

Цена: 100 единиц за 1000 токенов.
