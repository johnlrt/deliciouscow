use crate::auth::fetch_ws_access_token;
use crate::backend::{BackendChunk, InferenceBackend, InferenceRequest};
use crate::config::{compute_hwid_hash, NodeConfig};
use anyhow::{bail, Result};
use chrono::Utc;
use futures_util::{SinkExt, StreamExt};
use rand::Rng;
use reqwest::Client;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::{mpsc, Mutex};
use tokio_tungstenite::tungstenite::client::IntoClientRequest;
use tokio_tungstenite::tungstenite::Message;
use uuid::Uuid;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EnvelopeError {
    pub code: String,
    pub message: String,
    pub retryable: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Envelope {
    pub v: u8,
    #[serde(rename = "type")]
    pub msg_type: String,
    pub message_id: String,
    pub request_id: Option<String>,
    pub session_id: Option<String>,
    pub seq: u32,
    pub sent_at: String,
    pub payload: Value,
    pub error: Option<EnvelopeError>,
}

#[derive(Debug, Clone, Serialize)]
pub struct HardwareReport {
    pub cpu_model: String,
    pub cpu_cores: usize,
    pub ram_bytes: u64,
    pub os: String,
    pub arch: String,
}

pub fn local_hardware() -> HardwareReport {
    let cores = std::thread::available_parallelism()
        .map(|n| n.get())
        .unwrap_or(1);
    HardwareReport {
        cpu_model: "generic-cpu".to_string(),
        cpu_cores: cores,
        ram_bytes: 8 * 1024 * 1024 * 1024,
        os: std::env::consts::OS.to_string(),
        arch: std::env::consts::ARCH.to_string(),
    }
}

pub async fn run_node_loop<B: InferenceBackend + 'static>(
    cfg: NodeConfig,
    backend: Arc<B>,
    mut shutdown: tokio::sync::watch::Receiver<bool>,
) -> Result<()> {
    let http = Client::new();
    let mut attempt: u32 = 0;

    loop {
        if *shutdown.borrow() {
            return Ok(());
        }
        let res = run_one_session(&http, &cfg, backend.clone(), &mut shutdown).await;
        if *shutdown.borrow() {
            return Ok(());
        }
        match res {
            Ok(()) => attempt = 0,
            Err(e) => {
                eprintln!("ws session ended: {e:#}");
                attempt = attempt.saturating_add(1);
            }
        }
        let max_ms = (1000u64.saturating_mul(1u64 << attempt.min(6))).min(60_000);
        let jitter = rand::thread_rng().gen_range(0..=max_ms);
        tokio::select! {
            _ = tokio::time::sleep(Duration::from_millis(jitter)) => {}
            _ = shutdown.changed() => {
                if *shutdown.borrow() {
                    return Ok(());
                }
            }
        }
    }
}

pub async fn run_one_session<B: InferenceBackend + 'static>(
    http: &Client,
    cfg: &NodeConfig,
    backend: Arc<B>,
    shutdown: &mut tokio::sync::watch::Receiver<bool>,
) -> Result<()> {
    let jwt =
        fetch_ws_access_token(http, &cfg.server_origin, &cfg.node_id, &cfg.node_secret).await?;
    let ws_base = cfg
        .server_origin
        .trim_end_matches('/')
        .replacen("https://", "wss://", 1)
        .replacen("http://", "ws://", 1);
    let ws_url = format!("{}/api/node/ws", ws_base);

    let mut req = ws_url.into_client_request()?;
    req.headers_mut()
        .insert("Authorization", format!("Bearer {}", jwt).parse()?);

    let (ws_stream, _) = tokio_tungstenite::connect_async(req).await?;
    let (mut sink, mut stream) = ws_stream.split();

    let (out_tx, mut out_rx) = mpsc::channel::<(String, Option<String>, Value)>(64);
    let hwid = compute_hwid_hash(&cfg.install_salt);

    // Send hello (seq=1, session_id=None)
    let hello = Envelope {
        v: 1,
        msg_type: "hello".to_string(),
        message_id: Uuid::new_v4().to_string(),
        request_id: None,
        session_id: None,
        seq: 1,
        sent_at: Utc::now().to_rfc3339(),
        payload: serde_json::json!({
            "node_id": cfg.node_id,
            "node_version": env!("CARGO_PKG_VERSION"),
            "protocol_versions": [1],
            "hardware": local_hardware(),
            "hwid_hash": hwid,
        }),
        error: None,
    };
    sink.send(Message::Text(serde_json::to_string(&hello)?))
        .await?;

    // Wait for welcome
    let welcome_msg = tokio::time::timeout(Duration::from_secs(5), stream.next()).await?;
    let welcome_text = match welcome_msg {
        Some(Ok(Message::Text(t))) => t,
        other => bail!("expected welcome text frame, got {:?}", other),
    };
    let welcome: Envelope = serde_json::from_str(&welcome_text)?;
    if welcome.msg_type != "welcome" {
        bail!("expected welcome, got {}", welcome.msg_type);
    }
    let session_id = welcome
        .session_id
        .ok_or_else(|| anyhow::anyhow!("welcome missing session_id"))?;

    let active_tasks: Arc<Mutex<HashMap<String, tokio::sync::watch::Sender<bool>>>> =
        Arc::new(Mutex::new(HashMap::new()));

    // Outgoing writer task
    let sid_clone = session_id.clone();
    let writer = tokio::spawn(async move {
        let mut seq: u32 = 1; // hello was seq=1
        while let Some((msg_type, req_id, payload)) = out_rx.recv().await {
            seq = seq.saturating_add(1);
            let env = Envelope {
                v: 1,
                msg_type,
                message_id: Uuid::new_v4().to_string(),
                request_id: req_id,
                session_id: Some(sid_clone.clone()),
                seq,
                sent_at: Utc::now().to_rfc3339(),
                payload,
                error: None,
            };
            if let Ok(txt) = serde_json::to_string(&env) {
                if sink.send(Message::Text(txt)).await.is_err() {
                    break;
                }
            }
        }
    });

    let mut hb = tokio::time::interval(Duration::from_secs(15));
    hb.tick().await;

    loop {
        tokio::select! {
            _ = shutdown.changed() => {
                if *shutdown.borrow() {
                    let _ = out_tx.send(("goodbye".to_string(), None, serde_json::json!({
                        "reason": "shutdown",
                        "active_leases": [],
                    }))).await;
                    break;
                }
            }
            _ = hb.tick() => {
                let leases: Vec<String> = active_tasks.lock().await.keys().cloned().collect();
                let _ = out_tx.send(("heartbeat".to_string(), None, serde_json::json!({
                    "active_leases": leases,
                    "hardware": local_hardware(),
                    "models": backend.models(),
                }))).await;
            }
            frame = stream.next() => {
                let msg = match frame {
                    Some(Ok(Message::Text(t))) => t,
                    Some(Ok(Message::Close(_))) | None => break,
                    Some(Ok(_)) => continue,
                    Some(Err(e)) => {
                        writer.abort();
                        return Err(e.into());
                    }
                };
                let env: Envelope = match serde_json::from_str(&msg) {
                    Ok(e) => e,
                    Err(_) => continue,
                };
                match env.msg_type.as_str() {
                    "inference.start" => {
                        let attempt_id = env.payload["attempt_id"].as_str().unwrap_or_default().to_string();
                        let lease_id = env.payload["lease_id"].as_str().unwrap_or_default().to_string();
                        let model_id = env.payload["model_id"].as_str().unwrap_or_default().to_string();
                        let max_tokens = env.payload["max_tokens"].as_u64().unwrap_or(256) as u32;
                        let input_token_ids: Vec<i64> = env.payload["input_token_ids"]
                            .as_array()
                            .map(|a| a.iter().filter_map(|x| x.as_i64()).collect())
                            .unwrap_or_default();
                        let temperature = env.payload["parameters"]["temperature"].as_f64().unwrap_or(0.7);
                        let top_p = env.payload["parameters"]["top_p"].as_f64().unwrap_or(1.0);
                        let stop: Vec<String> = env.payload["parameters"]["stop"]
                            .as_array()
                            .map(|a| a.iter().filter_map(|x| x.as_str().map(|s| s.to_string())).collect())
                            .unwrap_or_default();

                        let (cancel_tx, mut cancel_rx) = tokio::sync::watch::channel(false);
                        active_tasks.lock().await.insert(attempt_id.clone(), cancel_tx);

                        let out = out_tx.clone();
                        let bk = backend.clone();
                        let tasks_map = active_tasks.clone();
                        let req_id = env.request_id.clone();

                        tokio::spawn(async move {
                            let _ = out.send(("inference.accepted".to_string(), req_id.clone(), serde_json::json!({
                                "attempt_id": attempt_id,
                                "lease_id": lease_id,
                            }))).await;

                            let (chunk_tx, mut chunk_rx) = mpsc::channel::<BackendChunk>(32);
                            let gen_req = InferenceRequest {
                                model_id,
                                input_token_ids,
                                max_tokens,
                                temperature,
                                top_p,
                                stop,
                            };
                            let gen_fut = tokio::spawn(async move {
                                let _ = bk.generate(gen_req, chunk_tx).await;
                            });

                            let mut chunk_idx: u32 = 0;
                            let mut last_idx: Option<u32> = None;
                            loop {
                                tokio::select! {
                                    _ = cancel_rx.changed() => {
                                        if *cancel_rx.borrow() {
                                            gen_fut.abort();
                                            let _ = out.send(("inference.cancelled".to_string(), req_id.clone(), serde_json::json!({
                                                "attempt_id": attempt_id,
                                                "lease_id": lease_id,
                                            }))).await;
                                            break;
                                        }
                                    }
                                    c = chunk_rx.recv() => {
                                        match c {
                                            Some(BackendChunk::Delta(text)) => {
                                                let _ = out.send(("inference.delta".to_string(), req_id.clone(), serde_json::json!({
                                                    "attempt_id": attempt_id,
                                                    "lease_id": lease_id,
                                                    "chunk_index": chunk_idx,
                                                    "text": text,
                                                }))).await;
                                                last_idx = Some(chunk_idx);
                                                chunk_idx += 1;
                                            }
                                            Some(BackendChunk::Done { finish_reason }) => {
                                                let _ = out.send(("inference.complete".to_string(), req_id.clone(), serde_json::json!({
                                                    "attempt_id": attempt_id,
                                                    "lease_id": lease_id,
                                                    "last_chunk_index": last_idx,
                                                    "finish_reason": finish_reason,
                                                    "claimed_input_tokens": 0,
                                                    "claimed_output_tokens": chunk_idx,
                                                }))).await;
                                                break;
                                            }
                                            Some(BackendChunk::Error(e)) => {
                                                eprintln!("backend error: {e}");
                                                let _ = out.send(("inference.complete".to_string(), req_id.clone(), serde_json::json!({
                                                    "attempt_id": attempt_id,
                                                    "lease_id": lease_id,
                                                    "last_chunk_index": last_idx,
                                                    "finish_reason": "stop",
                                                    "claimed_input_tokens": 0,
                                                    "claimed_output_tokens": chunk_idx,
                                                }))).await;
                                                break;
                                            }
                                            None => {
                                                let _ = out.send(("inference.complete".to_string(), req_id.clone(), serde_json::json!({
                                                    "attempt_id": attempt_id,
                                                    "lease_id": lease_id,
                                                    "last_chunk_index": last_idx,
                                                    "finish_reason": "stop",
                                                    "claimed_input_tokens": 0,
                                                    "claimed_output_tokens": chunk_idx,
                                                }))).await;
                                                break;
                                            }
                                        }
                                    }
                                }
                            }
                            tasks_map.lock().await.remove(&attempt_id);
                        });
                    }
                    "inference.cancel" => {
                        if let Some(aid) = env.payload["attempt_id"].as_str() {
                            if let Some(tx) = active_tasks.lock().await.get(aid) {
                                let _ = tx.send(true);
                            }
                        }
                    }
                    "drain" => {
                        break;
                    }
                    _ => {}
                }
            }
        }
    }
    writer.abort();
    Ok(())
}
