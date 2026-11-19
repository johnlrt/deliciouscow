# deliciouscow Community Node (`node/`)

Rust CLI (`cordova-node`) implementing Node WebSocket v1 and device-code enrollment (`#![forbid(unsafe_code)]`).

## Commands
```sh
# 1. Enroll the node with the coordinator
cargo run -- login --server http://127.0.0.1:8443 --name gpu-box

# 2a. Mock mode (separate deterministic mode, no model weights required)
cargo run -- run --backend mock --model cordova-tiny-chat

# 2b. Explicit llama.cpp mode — attach to an already-running llama-server
cargo run -- run --backend llama --llama-url http://127.0.0.1:8081 --model llama-3-8b-instruct

# 2c. Explicit llama.cpp mode — spawn a local llama-server binary + GGUF model file
cargo run -- run --backend llama --llama-bin /path/to/llama-server --llama-model /path/to/model.gguf --llama-port 8081 --model llama-3-8b-instruct

# 3. Inspect or remove local enrollment credentials
cargo run -- status
cargo run -- logout
```

In `--backend llama` mode, the node validates the endpoint URL or runner/model paths and waits for `GET /health` readiness (`--llama-ready-timeout-secs`, default `15`) before connecting to the coordinator.

## Checks
```sh
cargo fmt --check
cargo clippy -- -D warnings
cargo test
```

> `node/tests/llama_backend_integration.rs` uses a local HTTP test server that imitates `llama-server` (`/health`, `/completion`) to verify configuration validation, readiness retries, normal JSON responses, streaming chunks, error propagation, request cancellation, and server unavailability. It does **not** run a real LLM or verify model quality, inference speed, or memory usage.
