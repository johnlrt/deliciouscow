#![forbid(unsafe_code)]

use anyhow::{bail, Result};
use clap::{Parser, Subcommand, ValueEnum};
use cordova_node::{auth, backend, config, ws};
use reqwest::Client;
use std::path::PathBuf;
use std::sync::Arc;

#[derive(Copy, Clone, Debug, PartialEq, Eq, ValueEnum)]
enum BackendMode {
    /// Deterministic local mock backend (no real model weights)
    Mock,
    /// Explicit llama.cpp (llama-server) backend
    Llama,
}

#[derive(Parser)]
#[command(name = "cordova-node", about = "deliciouscow community inference node")]
struct Cli {
    #[arg(long, env = "CORDOVA_NODE_CONFIG")]
    config: Option<PathBuf>,

    #[command(subcommand)]
    command: Commands,
}

#[derive(Subcommand)]
enum Commands {
    /// Enroll this node using device-code flow
    Login {
        #[arg(long, default_value = "http://127.0.0.1:8443")]
        server: String,
        #[arg(long, default_value = "my-node")]
        name: String,
    },
    /// Run the node and connect to the coordinator via WebSocket
    Run {
        #[arg(long, value_enum, default_value_t = BackendMode::Mock)]
        backend: BackendMode,
        /// Existing llama-server HTTP endpoint (mutually exclusive with --llama-bin/--llama-model)
        #[arg(long)]
        llama_url: Option<String>,
        /// Path to local llama-server executable (must be paired with --llama-model)
        #[arg(long)]
        llama_bin: Option<PathBuf>,
        /// Path to local GGUF model file (must be paired with --llama-bin)
        #[arg(long)]
        llama_model: Option<PathBuf>,
        /// Loopback port when spawning a managed local llama-server
        #[arg(long, default_value_t = 8081)]
        llama_port: u16,
        /// Seconds to wait for llama-server /health readiness before failing
        #[arg(long, default_value_t = 15)]
        llama_ready_timeout_secs: u64,
        #[arg(long, default_value = "cordova-tiny-chat")]
        model: String,
    },
    /// Show enrolled node status
    Status,
    /// Remove local node credentials
    Logout,
}

#[tokio::main]
async fn main() -> Result<()> {
    let cli = Cli::parse();
    let cfg_path = cli.config.unwrap_or_else(config::default_config_path);

    match cli.command {
        Commands::Login { server, name } => {
            let salt = config::generate_or_load_salt(&cfg_path);
            let hwid = config::compute_hwid_hash(&salt);
            let http = Client::new();
            let enr = auth::start_enrollment(&http, &server, &hwid, &name).await?;
            println!(
                "Open {} and enter code: {}",
                enr.verification_uri, enr.user_code
            );
            println!("Waiting for approval (expires at {})...", enr.expires_at);
            let tok = auth::poll_enrollment_token(
                &http,
                &server,
                &enr.device_code,
                enr.poll_interval_seconds,
            )
            .await?;
            config::save_config(
                &cfg_path,
                &config::NodeConfig {
                    server_origin: tok.server_origin,
                    node_id: tok.node_id.clone(),
                    node_secret: tok.node_secret,
                    install_salt: salt,
                },
            )?;
            println!(
                "Enrolled node {} (credentials saved with 0600 to {})",
                tok.node_id,
                cfg_path.display()
            );
        }
        Commands::Run {
            backend,
            llama_url,
            llama_bin,
            llama_model,
            llama_port,
            llama_ready_timeout_secs,
            model,
        } => {
            let cfg = config::load_config(&cfg_path)?;
            let (shut_tx, shut_rx) = tokio::sync::watch::channel(false);
            tokio::spawn(async move {
                let _ = tokio::signal::ctrl_c().await;
                let _ = shut_tx.send(true);
            });
            match backend {
                BackendMode::Llama => {
                    let llama_cfg = backend::LlamaConfig::from_args(
                        llama_url,
                        llama_bin,
                        llama_model,
                        llama_port,
                        model,
                        llama_ready_timeout_secs,
                    )?;
                    println!(
                        "Starting llama.cpp backend for model {} (waiting for /health readiness)...",
                        llama_cfg.model_id
                    );
                    let bk =
                        Arc::new(backend::LlamaServerBackend::start_from_config(&llama_cfg).await?);
                    println!(
                        "llama-server ready at {}; connecting node {} -> {}",
                        bk.endpoint, cfg.node_id, cfg.server_origin
                    );
                    ws::run_node_loop(cfg, bk, shut_rx).await?;
                }
                BackendMode::Mock => {
                    if llama_url.is_some() || llama_bin.is_some() || llama_model.is_some() {
                        bail!(
                            "llama.cpp options (--llama-url, --llama-bin, --llama-model) cannot be used with --backend mock; pass --backend llama explicitly"
                        );
                    }
                    println!(
                        "Starting cordova-node {} (mock backend) -> {}",
                        cfg.node_id, cfg.server_origin
                    );
                    let bk = Arc::new(backend::MockBackend {
                        model_ids: vec![model],
                        token_delay_ms: 20,
                    });
                    ws::run_node_loop(cfg, bk, shut_rx).await?;
                }
            }
        }
        Commands::Status => match config::load_config(&cfg_path) {
            Ok(c) => println!("Enrolled node_id={} server={}", c.node_id, c.server_origin),
            Err(_) => println!("Not enrolled (no config at {})", cfg_path.display()),
        },
        Commands::Logout => {
            if cfg_path.exists() {
                std::fs::remove_file(&cfg_path)?;
                println!("Removed {}", cfg_path.display());
            } else {
                println!("Not enrolled");
            }
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::backend::InferenceBackend;
    use futures_util::{SinkExt, StreamExt};
    use tokio::net::TcpListener;
    use tokio_tungstenite::tungstenite::Message;

    #[test]
    fn test_config_save_load_permissions() {
        let dir = std::env::temp_dir().join(format!("cordova-node-test-{}", uuid::Uuid::new_v4()));
        let path = dir.join("config.json");
        let cfg = config::NodeConfig {
            server_origin: "http://127.0.0.1:8443".into(),
            node_id: "n-1".into(),
            node_secret: "sec-1".into(),
            install_salt: "salt-1".into(),
        };
        config::save_config(&path, &cfg).unwrap();
        let loaded = config::load_config(&path).unwrap();
        assert_eq!(loaded.node_id, "n-1");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&path).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o600);
        }
        let _ = std::fs::remove_dir_all(dir);
    }

    #[test]
    fn test_hmac_proof_deterministic() {
        let p1 = auth::compute_proof("secret123", "node-1", "nonce-abc").unwrap();
        let p2 = auth::compute_proof("secret123", "node-1", "nonce-abc").unwrap();
        assert_eq!(p1, p2);
        assert_eq!(p1.len(), 64);
    }

    #[tokio::test]
    async fn test_mock_backend_streams() {
        let bk = backend::MockBackend {
            model_ids: vec!["cordova-tiny-chat".into()],
            token_delay_ms: 0,
        };
        let (tx, mut rx) = tokio::sync::mpsc::channel(16);
        bk.generate(
            backend::InferenceRequest {
                model_id: "cordova-tiny-chat".into(),
                input_token_ids: vec![1, 2, 3],
                tokenization_verified: false,
                max_tokens: 32,
                temperature: 0.7,
                top_p: 1.0,
                stop: vec![],
            },
            tx,
        )
        .await
        .unwrap();

        let mut out = String::new();
        let mut done = false;
        while let Some(c) = rx.recv().await {
            match c {
                backend::BackendChunk::Delta(t) => out.push_str(&t),
                backend::BackendChunk::Done { .. } => {
                    done = true;
                    break;
                }
                backend::BackendChunk::Error(e) => panic!("unexpected err: {}", e),
            }
        }
        assert!(done);
        assert!(out.contains("Cordova"));
    }

    #[tokio::test]
    async fn test_ws_envelope_roundtrip_with_fake_server() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server_task = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            let mut ws = tokio_tungstenite::accept_async(stream).await.unwrap();
            let hello = ws.next().await.unwrap().unwrap();
            let env: ws::Envelope = serde_json::from_str(hello.to_text().unwrap()).unwrap();
            assert_eq!(env.msg_type, "hello");
            assert_eq!(env.seq, 1);

            let welcome = ws::Envelope {
                v: 1,
                msg_type: "welcome".into(),
                message_id: uuid::Uuid::new_v4().to_string(),
                request_id: None,
                session_id: Some("sess-1".into()),
                seq: 1,
                sent_at: chrono::Utc::now().to_rfc3339(),
                payload: serde_json::json!({"session_id": "sess-1"}),
                error: None,
            };
            ws.send(Message::Text(serde_json::to_string(&welcome).unwrap()))
                .await
                .unwrap();

            let start = ws::Envelope {
                v: 1,
                msg_type: "inference.start".into(),
                message_id: uuid::Uuid::new_v4().to_string(),
                request_id: Some("req-1".into()),
                session_id: Some("sess-1".into()),
                seq: 2,
                sent_at: chrono::Utc::now().to_rfc3339(),
                payload: serde_json::json!({
                    "attempt_id": "att-1",
                    "lease_id": "lease-1",
                    "model_id": "cordova-tiny-chat",
                    "input_token_ids": [10, 20],
                    "tokenization_verified": false,
                    "max_tokens": 16,
                    "parameters": {"temperature": 0.5, "top_p": 1.0, "stop": []}
                }),
                error: None,
            };
            ws.send(Message::Text(serde_json::to_string(&start).unwrap()))
                .await
                .unwrap();

            let mut got_complete = false;
            while let Some(Ok(Message::Text(txt))) = ws.next().await {
                let msg: ws::Envelope = serde_json::from_str(&txt).unwrap();
                if msg.msg_type == "inference.complete" {
                    got_complete = true;
                    break;
                }
            }
            assert!(got_complete);
        });

        // Connect directly to fake WS using tokio-tungstenite
        let url = format!("ws://{}/api/node/ws", addr);
        let (ws_stream, _) = tokio_tungstenite::connect_async(url).await.unwrap();
        let (mut sink, mut stream) = ws_stream.split();
        let hello = ws::Envelope {
            v: 1,
            msg_type: "hello".into(),
            message_id: uuid::Uuid::new_v4().to_string(),
            request_id: None,
            session_id: None,
            seq: 1,
            sent_at: chrono::Utc::now().to_rfc3339(),
            payload: serde_json::json!({"node_id": "n1"}),
            error: None,
        };
        sink.send(Message::Text(serde_json::to_string(&hello).unwrap()))
            .await
            .unwrap();
        let _w = stream.next().await.unwrap().unwrap();
        let _s = stream.next().await.unwrap().unwrap();
        let comp = ws::Envelope {
            v: 1,
            msg_type: "inference.complete".into(),
            message_id: uuid::Uuid::new_v4().to_string(),
            request_id: Some("req-1".into()),
            session_id: Some("sess-1".into()),
            seq: 2,
            sent_at: chrono::Utc::now().to_rfc3339(),
            payload: serde_json::json!({"attempt_id": "att-1", "finish_reason": "stop"}),
            error: None,
        };
        sink.send(Message::Text(serde_json::to_string(&comp).unwrap()))
            .await
            .unwrap();
        server_task.await.unwrap();
    }
}
