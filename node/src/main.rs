#![forbid(unsafe_code)]

mod auth;
mod backend;
mod config;
mod ws;

use anyhow::Result;
use clap::{Parser, Subcommand};
use reqwest::Client;
use std::path::PathBuf;
use std::sync::Arc;

#[derive(Parser)]
#[command(name = "cordova-node", about = "Cordova community inference node")]
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
        #[arg(long, default_value = "mock")]
        backend: String,
        #[arg(long, default_value = "http://127.0.0.1:8081")]
        llama_url: String,
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
            model,
        } => {
            let cfg = config::load_config(&cfg_path)?;
            let (shut_tx, shut_rx) = tokio::sync::watch::channel(false);
            tokio::spawn(async move {
                let _ = tokio::signal::ctrl_c().await;
                let _ = shut_tx.send(true);
            });
            println!(
                "Starting cordova-node {} -> {}",
                cfg.node_id, cfg.server_origin
            );
            if backend == "llama" {
                let bk = Arc::new(backend::LlamaServerBackend::connect_existing(
                    llama_url, model,
                ));
                ws::run_node_loop(cfg, bk, shut_rx).await?;
            } else {
                let bk = Arc::new(backend::MockBackend {
                    model_ids: vec![model],
                    token_delay_ms: 20,
                });
                ws::run_node_loop(cfg, bk, shut_rx).await?;
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
