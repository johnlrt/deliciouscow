# Cordova Infrastructure (`infra/`)

Docker Compose v2 stack for M2/pilot deployment:
- `nginx`: TLS termination on `127.0.0.1:8443` with unbuffered SSE (`/v1/chat/completions`) and WebSocket upgrade (`/api/node/ws`, `/api/live`).
- `server`: `cordova-server` serving the Svelte static build and API.
- `redis`: Redis 7.4 with AOF enabled (`WAITAOF`).
- `kafka`: Apache Kafka in KRaft mode for C7 event topics.
- `monero-wallet-rpc`: Stagenet Monero wallet RPC on internal `data` network.

## Usage
Generate self-signed dev certs in `.local/certs/server.{crt,key}` and run:
```sh
docker compose -f infra/compose.yaml up --build
```
