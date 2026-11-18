use anyhow::Result;
use futures_util::StreamExt;
use reqwest::Client;
use serde::{Deserialize, Serialize};
use std::process::Stdio;
use std::time::Duration;
use tokio::process::{Child, Command};
use tokio::sync::mpsc;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NodeModelInfo {
    pub model_id: String,
    pub state: String,
    pub artifact_sha256: Option<String>,
    pub active_slots: u32,
    pub max_slots: u32,
}

#[derive(Debug, Clone)]
pub struct InferenceRequest {
    pub model_id: String,
    pub input_token_ids: Vec<i64>,
    pub max_tokens: u32,
    pub temperature: f64,
    pub top_p: f64,
    pub stop: Vec<String>,
}

#[derive(Debug, Clone)]
pub enum BackendChunk {
    Delta(String),
    Done { finish_reason: String },
    Error(String),
}

pub trait InferenceBackend: Send + Sync {
    fn models(&self) -> Vec<NodeModelInfo>;
    fn generate(
        &self,
        req: InferenceRequest,
        tx: mpsc::Sender<BackendChunk>,
    ) -> impl std::future::Future<Output = Result<()>> + Send;
}

pub struct MockBackend {
    pub model_ids: Vec<String>,
    pub token_delay_ms: u64,
}

impl Default for MockBackend {
    fn default() -> Self {
        Self {
            model_ids: vec!["cordova-tiny-chat".to_string()],
            token_delay_ms: 25,
        }
    }
}

impl InferenceBackend for MockBackend {
    fn models(&self) -> Vec<NodeModelInfo> {
        self.model_ids
            .iter()
            .map(|id| NodeModelInfo {
                model_id: id.clone(),
                state: "loaded".to_string(),
                artifact_sha256: None,
                active_slots: 0,
                max_slots: 4,
            })
            .collect()
    }

    async fn generate(&self, req: InferenceRequest, tx: mpsc::Sender<BackendChunk>) -> Result<()> {
        let words = [
            "Hello",
            " from",
            " Cordova",
            " Rust",
            " node",
            " (",
            &req.model_id,
            ")!",
        ];
        for w in words {
            if tx.send(BackendChunk::Delta(w.to_string())).await.is_err() {
                return Ok(());
            }
            if self.token_delay_ms > 0 {
                tokio::time::sleep(Duration::from_millis(self.token_delay_ms)).await;
            }
        }
        let _ = tx
            .send(BackendChunk::Done {
                finish_reason: "stop".to_string(),
            })
            .await;
        Ok(())
    }
}

/// Manages a llama-server subprocess (or connects to an existing OpenAI/llama.cpp HTTP endpoint).
pub struct LlamaServerBackend {
    pub endpoint: String,
    pub model_id: String,
    client: Client,
    _child: Option<tokio::sync::Mutex<Child>>,
}

impl LlamaServerBackend {
    pub fn connect_existing(endpoint: String, model_id: String) -> Self {
        Self {
            endpoint: endpoint.trim_end_matches('/').to_string(),
            model_id,
            client: Client::new(),
            _child: None,
        }
    }

    #[allow(dead_code)]
    pub async fn spawn(bin: &str, model_path: &str, model_id: String, port: u16) -> Result<Self> {
        let child = Command::new(bin)
            .args([
                "-m",
                model_path,
                "--port",
                &port.to_string(),
                "--host",
                "127.0.0.1",
            ])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .kill_on_drop(true)
            .spawn()?;

        let endpoint = format!("http://127.0.0.1:{}", port);
        let client = Client::new();
        for _ in 0..30 {
            if let Ok(r) = client.get(format!("{}/health", endpoint)).send().await {
                if r.status().is_success() {
                    break;
                }
            }
            tokio::time::sleep(Duration::from_millis(500)).await;
        }
        Ok(Self {
            endpoint,
            model_id,
            client,
            _child: Some(tokio::sync::Mutex::new(child)),
        })
    }
}

impl InferenceBackend for LlamaServerBackend {
    fn models(&self) -> Vec<NodeModelInfo> {
        vec![NodeModelInfo {
            model_id: self.model_id.clone(),
            state: "loaded".to_string(),
            artifact_sha256: None,
            active_slots: 0,
            max_slots: 2,
        }]
    }

    async fn generate(&self, req: InferenceRequest, tx: mpsc::Sender<BackendChunk>) -> Result<()> {
        // Call llama-server /completion with token IDs per C9
        let body = serde_json::json!({
            "prompt": req.input_token_ids,
            "n_predict": req.max_tokens,
            "temperature": req.temperature,
            "top_p": req.top_p,
            "stop": req.stop,
            "stream": true,
        });
        let resp = match self
            .client
            .post(format!("{}/completion", self.endpoint))
            .json(&body)
            .send()
            .await
        {
            Ok(r) if r.status().is_success() => r,
            Ok(r) => {
                let _ = tx
                    .send(BackendChunk::Error(format!(
                        "llama-server returned {}",
                        r.status()
                    )))
                    .await;
                return Ok(());
            }
            Err(e) => {
                let _ = tx.send(BackendChunk::Error(e.to_string())).await;
                return Ok(());
            }
        };

        let mut stream = resp.bytes_stream();
        let mut buf = String::new();
        while let Some(item) = stream.next().await {
            let bytes = match item {
                Ok(b) => b,
                Err(e) => {
                    let _ = tx.send(BackendChunk::Error(e.to_string())).await;
                    return Ok(());
                }
            };
            buf.push_str(&String::from_utf8_lossy(&bytes));
            while let Some(pos) = buf.find('\n') {
                let line = buf[..pos].trim().to_string();
                buf = buf[pos + 1..].to_string();
                if let Some(rest) = line.strip_prefix("data:") {
                    let rest = rest.trim();
                    if rest == "[DONE]" {
                        continue;
                    }
                    if let Ok(v) = serde_json::from_str::<serde_json::Value>(rest) {
                        if let Some(c) = v.get("content").and_then(|x| x.as_str()) {
                            if !c.is_empty()
                                && tx.send(BackendChunk::Delta(c.to_string())).await.is_err()
                            {
                                return Ok(());
                            }
                        }
                        if v.get("stop").and_then(|x| x.as_bool()).unwrap_or(false) {
                            let _ = tx
                                .send(BackendChunk::Done {
                                    finish_reason: "stop".to_string(),
                                })
                                .await;
                            return Ok(());
                        }
                    }
                }
            }
        }
        let _ = tx
            .send(BackendChunk::Done {
                finish_reason: "stop".to_string(),
            })
            .await;
        Ok(())
    }
}
