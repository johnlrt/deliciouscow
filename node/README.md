# Cordova Community Node (`node/`)

Rust CLI (`cordova-node`) implementing C3 Node WebSocket v1 and C2 device-code enrollment (`#![forbid(unsafe_code)]`).

## Commands
```sh
cargo run -- login --server http://127.0.0.1:8443 --name gpu-box
cargo run -- run --backend mock --model cordova-tiny-chat
cargo run -- run --backend llama --llama-url http://127.0.0.1:8081 --model llama-3-8b-instruct
cargo run -- status
cargo run -- logout
```

## Checks
```sh
cargo fmt --check
cargo clippy -- -D warnings
cargo test
```
