use anyhow::{bail, Context, Result};
use futures_util::StreamExt;
use reqwest::{Client, StatusCode, Url};
use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::{Duration, Instant};
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
    pub tokenization_verified: bool,
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

/// Validated startup configuration for the `llama.cpp` (`llama-server`) backend.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum LlamaMode {
    /// Connect to an already-running `llama-server` HTTP endpoint.
    Endpoint { url: String },
    /// Spawn a local `llama-server` binary with the given GGUF model file on loopback.
    Managed {
        bin_path: PathBuf,
        model_path: PathBuf,
        port: u16,
    },
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LlamaConfig {
    pub mode: LlamaMode,
    pub model_id: String,
    pub ready_timeout: Duration,
}

impl LlamaConfig {
    pub fn from_args(
        llama_url: Option<String>,
        llama_bin: Option<PathBuf>,
        llama_model: Option<PathBuf>,
        llama_port: u16,
        model_id: String,
        ready_timeout_secs: u64,
    ) -> Result<Self> {
        let model_id = model_id.trim().to_string();
        if model_id.is_empty() {
            bail!("--model must not be empty when running with --backend llama");
        }
        if ready_timeout_secs == 0 {
            bail!("--llama-ready-timeout-secs must be greater than 0");
        }

        let has_url = llama_url
            .as_deref()
            .map(|s| !s.trim().is_empty())
            .unwrap_or(false);
        let has_bin = llama_bin.is_some();
        let has_model = llama_model.is_some();

        if has_url && (has_bin || has_model) {
            bail!(
                "ambiguous llama.cpp configuration: specify either --llama-url OR (--llama-bin and --llama-model), not both"
            );
        }

        let mode = if has_url {
            let raw = llama_url.unwrap_or_default();
            let validated = validate_llama_url(&raw)?;
            LlamaMode::Endpoint { url: validated }
        } else if has_bin || has_model {
            let bin_path = llama_bin.ok_or_else(|| {
                anyhow::anyhow!(
                    "--llama-model requires --llama-bin <PATH_TO_LLAMA_SERVER> (or use --llama-url for an already-running llama-server)"
                )
            })?;
            let model_path = llama_model.ok_or_else(|| {
                anyhow::anyhow!(
                    "--llama-bin requires --llama-model <PATH_TO_MODEL.gguf> (or use --llama-url for an already-running llama-server)"
                )
            })?;
            if llama_port == 0 {
                bail!("--llama-port must be between 1 and 65535");
            }
            validate_runner_binary(&bin_path)?;
            validate_model_file(&model_path)?;
            LlamaMode::Managed {
                bin_path,
                model_path,
                port: llama_port,
            }
        } else {
            bail!(
                "--backend llama requires either --llama-url <URL> (e.g. http://127.0.0.1:8081) or both --llama-bin <PATH> and --llama-model <PATH>"
            );
        };

        Ok(Self {
            mode,
            model_id,
            ready_timeout: Duration::from_secs(ready_timeout_secs),
        })
    }
}

pub fn validate_llama_url(raw: &str) -> Result<String> {
    let trimmed = raw.trim();
    let parsed = Url::parse(trimmed).with_context(|| {
        format!(
            "invalid --llama-url {:?}: expected http:// or https:// URL (for example http://127.0.0.1:8081)",
            raw
        )
    })?;
    match parsed.scheme() {
        "http" | "https" => {}
        other => bail!(
            "invalid --llama-url {:?}: unsupported scheme {:?} (only http and https are allowed)",
            raw,
            other
        ),
    }
    if parsed.host_str().unwrap_or_default().is_empty() {
        bail!("invalid --llama-url {:?}: missing host", raw);
    }
    Ok(trimmed.trim_end_matches('/').to_string())
}

fn validate_runner_binary(path: &Path) -> Result<()> {
    let meta = std::fs::metadata(path).with_context(|| {
        format!(
            "llama.cpp runner binary not found at {} (pass a valid --llama-bin path)",
            path.display()
        )
    })?;
    if !meta.is_file() {
        bail!("--llama-bin path {} is not a regular file", path.display());
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        if meta.permissions().mode() & 0o111 == 0 {
            bail!(
                "--llama-bin path {} is not executable (chmod +x required)",
                path.display()
            );
        }
    }
    Ok(())
}

fn validate_model_file(path: &Path) -> Result<()> {
    let meta = std::fs::metadata(path).with_context(|| {
        format!(
            "model file not found at {} (pass a valid --llama-model GGUF path)",
            path.display()
        )
    })?;
    if !meta.is_file() {
        bail!(
            "--llama-model path {} is not a regular file",
            path.display()
        );
    }
    if meta.len() == 0 {
        bail!("--llama-model file {} is empty", path.display());
    }
    Ok(())
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum HealthState {
    Ready,
    Loading(String),
    Unhealthy(StatusCode, String),
    Unreachable(String),
}

/// Manages a `llama-server` subprocess or connects to an existing `llama.cpp` HTTP endpoint.
pub struct LlamaServerBackend {
    pub endpoint: String,
    pub model_id: String,
    client: Client,
    child: Option<tokio::sync::Mutex<Child>>,
}

impl LlamaServerBackend {
    pub fn connect_existing(endpoint: String, model_id: String) -> Self {
        Self {
            endpoint: endpoint.trim_end_matches('/').to_string(),
            model_id,
            client: Client::new(),
            child: None,
        }
    }

    /// Start or connect to `llama-server` according to validated `LlamaConfig` and verify `/health` readiness.
    pub async fn start_from_config(cfg: &LlamaConfig) -> Result<Self> {
        match &cfg.mode {
            LlamaMode::Endpoint { url } => {
                let bk = Self::connect_existing(url.clone(), cfg.model_id.clone());
                bk.wait_until_ready(cfg.ready_timeout, false).await?;
                Ok(bk)
            }
            LlamaMode::Managed {
                bin_path,
                model_path,
                port,
            } => {
                let child = Command::new(bin_path)
                    .arg("-m")
                    .arg(model_path)
                    .arg("--port")
                    .arg(port.to_string())
                    .arg("--host")
                    .arg("127.0.0.1")
                    .stdout(Stdio::null())
                    .stderr(Stdio::null())
                    .kill_on_drop(true)
                    .spawn()
                    .with_context(|| {
                        format!(
                            "failed to spawn llama-server binary {} with model {}",
                            bin_path.display(),
                            model_path.display()
                        )
                    })?;

                let bk = Self {
                    endpoint: format!("http://127.0.0.1:{}", port),
                    model_id: cfg.model_id.clone(),
                    client: Client::new(),
                    child: Some(tokio::sync::Mutex::new(child)),
                };
                bk.wait_until_ready(cfg.ready_timeout, true).await?;
                Ok(bk)
            }
        }
    }

    async fn probe_health(&self) -> HealthState {
        let url = format!("{}/health", self.endpoint);
        let resp = match self
            .client
            .get(&url)
            .timeout(Duration::from_secs(2))
            .send()
            .await
        {
            Ok(r) => r,
            Err(e) => return HealthState::Unreachable(e.to_string()),
        };

        let status = resp.status();
        let body = resp.text().await.unwrap_or_default();
        if status.is_success() {
            HealthState::Ready
        } else if status == StatusCode::SERVICE_UNAVAILABLE {
            HealthState::Loading(body)
        } else {
            HealthState::Unhealthy(status, body)
        }
    }

    /// Perform a single readiness check against `GET <endpoint>/health`.
    pub async fn check_ready(&self) -> Result<()> {
        match self.probe_health().await {
            HealthState::Ready => Ok(()),
            HealthState::Loading(body) => bail!(
                "llama-server at {} is not ready yet (HTTP 503 loading model): {}",
                self.endpoint,
                body.trim()
            ),
            HealthState::Unhealthy(status, body) => bail!(
                "llama-server health check at {}/health failed with HTTP {}: {}",
                self.endpoint,
                status,
                body.trim()
            ),
            HealthState::Unreachable(err) => {
                bail!("llama-server at {} is unreachable: {}", self.endpoint, err)
            }
        }
    }

    /// Poll `GET <endpoint>/health` until ready or until `timeout` elapses.
    pub async fn wait_until_ready(&self, timeout: Duration, retry_unreachable: bool) -> Result<()> {
        let deadline = Instant::now() + timeout;

        loop {
            if let Some(child_mutex) = &self.child {
                let mut child = child_mutex.lock().await;
                if let Some(status) = child.try_wait()? {
                    bail!(
                        "managed llama-server process exited before becoming ready (exit status: {})",
                        status
                    );
                }
            }

            let last_err = match self.probe_health().await {
                HealthState::Ready => return Ok(()),
                HealthState::Loading(body) => {
                    format!("HTTP 503 loading model ({})", body.trim())
                }
                HealthState::Unreachable(err) => {
                    if !retry_unreachable {
                        bail!("llama-server at {} is unreachable: {}", self.endpoint, err);
                    }
                    format!("unreachable ({})", err)
                }
                HealthState::Unhealthy(status, body) => {
                    bail!(
                        "llama-server health check at {}/health failed with HTTP {}: {}",
                        self.endpoint,
                        status,
                        body.trim()
                    );
                }
            };

            if Instant::now() >= deadline {
                bail!(
                    "llama-server at {} did not become ready within {}s (last state: {})",
                    self.endpoint,
                    timeout.as_secs(),
                    last_err
                );
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
    }
}

fn finish_reason_from_value(v: &serde_json::Value) -> String {
    if v.get("stopped_limit")
        .and_then(|x| x.as_bool())
        .unwrap_or(false)
    {
        "length".to_string()
    } else {
        "stop".to_string()
    }
}

fn extract_error_message(v: &serde_json::Value) -> Option<String> {
    let err = v.get("error")?;
    if let Some(s) = err.as_str() {
        return Some(s.to_string());
    }
    if let Some(m) = err.get("message").and_then(|x| x.as_str()) {
        return Some(m.to_string());
    }
    Some(err.to_string())
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
        if !req.tokenization_verified {
            let _ = tx
                .send(BackendChunk::Error(format!(
                    "refusing llama.cpp inference for model {}: server tokenization is unverified or synthetic",
                    req.model_id
                )))
                .await;
            return Ok(());
        }

        // Call llama-server /completion with verified token IDs per C9
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
                let status = r.status();
                let err_body = r.text().await.unwrap_or_default();
                let detail = err_body.trim();
                let msg = if detail.is_empty() {
                    format!("llama-server returned HTTP {}", status)
                } else {
                    format!("llama-server returned HTTP {}: {}", status, detail)
                };
                let _ = tx.send(BackendChunk::Error(msg)).await;
                return Ok(());
            }
            Err(e) => {
                let _ = tx
                    .send(BackendChunk::Error(format!(
                        "llama-server unreachable at {}: {}",
                        self.endpoint, e
                    )))
                    .await;
                return Ok(());
            }
        };

        let is_json = resp
            .headers()
            .get(reqwest::header::CONTENT_TYPE)
            .and_then(|v| v.to_str().ok())
            .map(|ct| ct.contains("application/json"))
            .unwrap_or(false);

        if is_json {
            let text = match resp.text().await {
                Ok(t) => t,
                Err(e) => {
                    let _ = tx
                        .send(BackendChunk::Error(format!(
                            "failed reading llama-server JSON response: {}",
                            e
                        )))
                        .await;
                    return Ok(());
                }
            };
            let v: serde_json::Value = match serde_json::from_str(&text) {
                Ok(v) => v,
                Err(e) => {
                    let _ = tx
                        .send(BackendChunk::Error(format!(
                            "invalid JSON from llama-server: {}",
                            e
                        )))
                        .await;
                    return Ok(());
                }
            };
            if let Some(err_msg) = extract_error_message(&v) {
                let _ = tx
                    .send(BackendChunk::Error(format!(
                        "llama-server error: {}",
                        err_msg
                    )))
                    .await;
                return Ok(());
            }
            if let Some(c) = v.get("content").and_then(|x| x.as_str()) {
                if !c.is_empty() && tx.send(BackendChunk::Delta(c.to_string())).await.is_err() {
                    return Ok(());
                }
            }
            if v.get("stop").and_then(|x| x.as_bool()) != Some(true) {
                let _ = tx
                    .send(BackendChunk::Error(
                        "llama-server JSON response missing terminal stop=true".to_string(),
                    ))
                    .await;
                return Ok(());
            }
            let _ = tx
                .send(BackendChunk::Done {
                    finish_reason: finish_reason_from_value(&v),
                })
                .await;
            return Ok(());
        }

        let mut stream = resp.bytes_stream();
        let mut buf = String::new();
        while let Some(item) = stream.next().await {
            let bytes = match item {
                Ok(b) => b,
                Err(e) => {
                    let _ = tx
                        .send(BackendChunk::Error(format!(
                            "llama-server stream error: {}",
                            e
                        )))
                        .await;
                    return Ok(());
                }
            };
            buf.push_str(&String::from_utf8_lossy(&bytes));
            while let Some(pos) = buf.find('\n') {
                let line = buf[..pos].trim().to_string();
                buf = buf[pos + 1..].to_string();
                if line.is_empty()
                    || line.starts_with(':')
                    || line.starts_with("event:")
                    || line.starts_with("id:")
                    || line.starts_with("retry:")
                {
                    continue;
                }
                let Some(rest) = line.strip_prefix("data:") else {
                    let _ = tx
                        .send(BackendChunk::Error(format!(
                            "malformed SSE line from llama-server: {:?}",
                            line
                        )))
                        .await;
                    return Ok(());
                };
                let rest = rest.trim();
                if rest == "[DONE]" {
                    let _ = tx
                        .send(BackendChunk::Error(
                            "llama-server stream ended with [DONE] without terminal stop=true event"
                                .to_string(),
                        ))
                        .await;
                    return Ok(());
                }
                let v = match serde_json::from_str::<serde_json::Value>(rest) {
                    Ok(v) => v,
                    Err(e) => {
                        let _ = tx
                            .send(BackendChunk::Error(format!(
                                "malformed SSE JSON from llama-server: {}",
                                e
                            )))
                            .await;
                        return Ok(());
                    }
                };
                if let Some(err_msg) = extract_error_message(&v) {
                    let _ = tx
                        .send(BackendChunk::Error(format!(
                            "llama-server stream error: {}",
                            err_msg
                        )))
                        .await;
                    return Ok(());
                }
                if let Some(c) = v.get("content").and_then(|x| x.as_str()) {
                    if !c.is_empty() && tx.send(BackendChunk::Delta(c.to_string())).await.is_err() {
                        return Ok(());
                    }
                }
                if v.get("stop").and_then(|x| x.as_bool()) == Some(true) {
                    let _ = tx
                        .send(BackendChunk::Done {
                            finish_reason: finish_reason_from_value(&v),
                        })
                        .await;
                    return Ok(());
                }
            }
        }
        let _ = tx
            .send(BackendChunk::Error(
                "llama-server stream ended prematurely without terminal stop=true event"
                    .to_string(),
            ))
            .await;
        Ok(())
    }
}
