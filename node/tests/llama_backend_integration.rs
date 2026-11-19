#![forbid(unsafe_code)]

//! Integration tests for `LlamaServerBackend` and `LlamaConfig` against a local test HTTP server
//! that imitates `llama.cpp` (`llama-server`) `/health` and `/completion` endpoints.
//!
//! NOTE: These tests use an in-process HTTP imitator (no real model weights or inference).
//! They verify protocol handling, configuration validation, readiness checks, streaming,
//! error propagation, cancellation, and server unavailability — NOT real LLM quality,
//! throughput, or memory consumption.

use cordova_node::backend::{
    BackendChunk, InferenceBackend, InferenceRequest, LlamaConfig, LlamaMode, LlamaServerBackend,
};
use cordova_node::config::NodeConfig;
use cordova_node::ws::{run_one_session, Envelope};
use futures_util::{SinkExt, StreamExt};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;
use tokio::sync::{mpsc, Notify};
use tokio_tungstenite::tungstenite::Message;

async fn spawn_fake_llama<F, Fut>(handler: F) -> (String, tokio::task::JoinHandle<()>)
where
    F: Fn(String, tokio::net::TcpStream) -> Fut + Send + Sync + 'static,
    Fut: std::future::Future<Output = ()> + Send + 'static,
{
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let handler = Arc::new(handler);
    let handle = tokio::spawn(async move {
        loop {
            let Ok((mut stream, _)) = listener.accept().await else {
                break;
            };
            let h = handler.clone();
            tokio::spawn(async move {
                let mut buf = vec![0u8; 8192];
                let n = stream.read(&mut buf).await.unwrap_or(0);
                if n == 0 {
                    return;
                }
                let req = String::from_utf8_lossy(&buf[..n]).to_string();
                let first_line = req.lines().next().unwrap_or("").to_string();
                h(first_line, stream).await;
            });
        }
    });
    (format!("http://{}", addr), handle)
}

fn sample_request() -> InferenceRequest {
    InferenceRequest {
        model_id: "llama-3-8b-instruct".to_string(),
        input_token_ids: vec![1, 42, 100],
        tokenization_verified: true,
        max_tokens: 32,
        temperature: 0.7,
        top_p: 1.0,
        stop: vec![],
    }
}

#[test]
fn test_llama_config_validation_errors_and_valid_modes() {
    // Neither endpoint nor runner+model
    let err = LlamaConfig::from_args(None, None, None, 8081, "llama-3-8b-instruct".into(), 15)
        .unwrap_err();
    assert!(
        err.to_string().contains("--llama-url") && err.to_string().contains("--llama-bin"),
        "unexpected error: {err}"
    );

    // Ambiguous: both --llama-url and --llama-bin
    let err = LlamaConfig::from_args(
        Some("http://127.0.0.1:8081".into()),
        Some("/nonexistent/llama-server".into()),
        None,
        8081,
        "llama-3-8b-instruct".into(),
        15,
    )
    .unwrap_err();
    assert!(err.to_string().contains("ambiguous"), "unexpected: {err}");

    // Invalid scheme
    let err = LlamaConfig::from_args(
        Some("ftp://127.0.0.1:8081".into()),
        None,
        None,
        8081,
        "llama-3-8b-instruct".into(),
        15,
    )
    .unwrap_err();
    assert!(
        err.to_string().contains("unsupported scheme"),
        "unexpected: {err}"
    );

    // Missing --llama-model when --llama-bin is provided
    let err = LlamaConfig::from_args(
        None,
        Some("/nonexistent/llama-server".into()),
        None,
        8081,
        "llama-3-8b-instruct".into(),
        15,
    )
    .unwrap_err();
    assert!(
        err.to_string().contains("--llama-model"),
        "unexpected: {err}"
    );

    // Non-existent runner binary
    let err = LlamaConfig::from_args(
        None,
        Some("/definitely/missing/llama-server".into()),
        Some("/definitely/missing/model.gguf".into()),
        8081,
        "llama-3-8b-instruct".into(),
        15,
    )
    .unwrap_err();
    assert!(
        err.to_string().contains("runner binary not found"),
        "unexpected: {err}"
    );

    // Valid endpoint configuration
    let cfg = LlamaConfig::from_args(
        Some("http://127.0.0.1:8081/".into()),
        None,
        None,
        8081,
        "llama-3-8b-instruct".into(),
        10,
    )
    .unwrap();
    assert_eq!(
        cfg.mode,
        LlamaMode::Endpoint {
            url: "http://127.0.0.1:8081".into()
        }
    );
    assert_eq!(cfg.ready_timeout, Duration::from_secs(10));

    // Valid managed runner + model file (using small temporary dummy files, no model weights)
    let tmp = std::env::temp_dir().join(format!("deliciouscow-llama-cfg-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&tmp).unwrap();
    let bin = tmp.join("fake-llama-server");
    let model = tmp.join("dummy.gguf");
    std::fs::write(&bin, b"#!/bin/sh\nexit 0\n").unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&bin, std::fs::Permissions::from_mode(0o755)).unwrap();
    }
    std::fs::write(&model, b"GGUF-placeholder").unwrap();

    let cfg = LlamaConfig::from_args(
        None,
        Some(bin.clone()),
        Some(model.clone()),
        18081,
        "llama-3-8b-instruct".into(),
        5,
    )
    .unwrap();
    assert_eq!(
        cfg.mode,
        LlamaMode::Managed {
            bin_path: bin,
            model_path: model,
            port: 18081,
        }
    );
    let _ = std::fs::remove_dir_all(&tmp);
}

#[tokio::test]
async fn test_fake_llama_readiness_retries_503_then_ready() {
    let attempts = Arc::new(AtomicUsize::new(0));
    let attempts_cl = attempts.clone();
    let (url, srv) = spawn_fake_llama(move |first_line, mut stream| {
        let attempts = attempts_cl.clone();
        async move {
            if first_line.starts_with("GET /health ") {
                let n = attempts.fetch_add(1, Ordering::SeqCst);
                if n < 2 {
                    let body = r#"{"error":{"code":503,"message":"Loading model"}}"#;
                    let resp = format!(
                        "HTTP/1.1 503 Service Unavailable\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                        body.len(),
                        body
                    );
                    let _ = stream.write_all(resp.as_bytes()).await;
                } else {
                    let body = r#"{"status":"ok"}"#;
                    let resp = format!(
                        "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                        body.len(),
                        body
                    );
                    let _ = stream.write_all(resp.as_bytes()).await;
                }
            }
        }
    })
    .await;

    let cfg = LlamaConfig::from_args(Some(url), None, None, 8081, "llama-3-8b-instruct".into(), 5)
        .unwrap();
    let bk = LlamaServerBackend::start_from_config(&cfg).await.unwrap();
    assert!(attempts.load(Ordering::SeqCst) >= 3);
    bk.check_ready().await.unwrap();
    srv.abort();
}

#[tokio::test]
async fn test_fake_llama_normal_json_response() {
    let (url, srv) = spawn_fake_llama(move |first_line, mut stream| async move {
        if first_line.starts_with("POST /completion ") {
            let body = r#"{"content":"Hello from fake llama JSON","stop":true}"#;
            let resp = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(),
                body
            );
            let _ = stream.write_all(resp.as_bytes()).await;
        }
    })
    .await;

    let bk = LlamaServerBackend::connect_existing(url, "llama-3-8b-instruct".into());
    let (tx, mut rx) = mpsc::channel(16);
    bk.generate(sample_request(), tx).await.unwrap();

    let mut text = String::new();
    let mut finish = None;
    while let Some(c) = rx.recv().await {
        match c {
            BackendChunk::Delta(d) => text.push_str(&d),
            BackendChunk::Done { finish_reason } => {
                finish = Some(finish_reason);
                break;
            }
            BackendChunk::Error(e) => panic!("unexpected error: {e}"),
        }
    }
    assert_eq!(text, "Hello from fake llama JSON");
    assert_eq!(finish.as_deref(), Some("stop"));
    srv.abort();
}

#[tokio::test]
async fn test_fake_llama_streaming_chunks() {
    let (url, srv) = spawn_fake_llama(move |first_line, mut stream| async move {
        if first_line.starts_with("POST /completion ") {
            let hdr =
                "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n";
            let _ = stream.write_all(hdr.as_bytes()).await;
            for chunk in [
                "data: {\"content\":\"Fake\",\"stop\":false}\n\n",
                "data: {\"content\":\" llama\",\"stop\":false}\n\n",
                "data: {\"content\":\" stream\",\"stop\":false}\n\n",
                "data: {\"content\":\"\",\"stop\":true}\n\n",
            ] {
                let _ = stream.write_all(chunk.as_bytes()).await;
                let _ = stream.flush().await;
                tokio::time::sleep(Duration::from_millis(10)).await;
            }
        }
    })
    .await;

    let bk = LlamaServerBackend::connect_existing(url, "llama-3-8b-instruct".into());
    let (tx, mut rx) = mpsc::channel(16);
    bk.generate(sample_request(), tx).await.unwrap();

    let mut deltas = Vec::new();
    let mut finish = None;
    while let Some(c) = rx.recv().await {
        match c {
            BackendChunk::Delta(d) => deltas.push(d),
            BackendChunk::Done { finish_reason } => {
                finish = Some(finish_reason);
                break;
            }
            BackendChunk::Error(e) => panic!("unexpected error: {e}"),
        }
    }
    assert_eq!(deltas, vec!["Fake", " llama", " stream"]);
    assert_eq!(finish.as_deref(), Some("stop"));
    srv.abort();
}

#[tokio::test]
async fn test_fake_llama_http_error_response() {
    let (url, srv) = spawn_fake_llama(move |first_line, mut stream| async move {
        if first_line.starts_with("POST /completion ") {
            let body = r#"{"error":{"code":500,"message":"simulated slot failure"}}"#;
            let resp = format!(
                "HTTP/1.1 500 Internal Server Error\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(),
                body
            );
            let _ = stream.write_all(resp.as_bytes()).await;
        }
    })
    .await;

    let bk = LlamaServerBackend::connect_existing(url, "llama-3-8b-instruct".into());
    let (tx, mut rx) = mpsc::channel(16);
    bk.generate(sample_request(), tx).await.unwrap();

    let chunk = rx.recv().await.expect("expected error chunk");
    match chunk {
        BackendChunk::Error(msg) => {
            assert!(msg.contains("500"), "expected HTTP 500 in error: {msg}");
            assert!(
                msg.contains("simulated slot failure"),
                "expected body detail in error: {msg}"
            );
        }
        other => panic!("expected BackendChunk::Error, got {:?}", other),
    }
    srv.abort();
}

#[tokio::test]
async fn test_fake_llama_server_unavailable() {
    // Bind and immediately drop a loopback port so connections are refused.
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let closed_addr = listener.local_addr().unwrap();
    drop(listener);

    let url = format!("http://{}", closed_addr);
    let bk = LlamaServerBackend::connect_existing(url, "llama-3-8b-instruct".into());

    let ready_err = bk.check_ready().await.unwrap_err();
    assert!(
        ready_err.to_string().contains("unreachable"),
        "unexpected readiness error: {ready_err}"
    );

    let (tx, mut rx) = mpsc::channel(16);
    bk.generate(sample_request(), tx).await.unwrap();
    match rx.recv().await.expect("expected error chunk") {
        BackendChunk::Error(msg) => {
            assert!(
                msg.contains("unreachable"),
                "expected unreachable error, got: {msg}"
            );
        }
        other => panic!("expected BackendChunk::Error, got {:?}", other),
    }
}

#[tokio::test]
async fn test_fake_llama_cancel_and_error_via_node_ws_session() {
    // 1. Fake llama-server that streams 1 chunk, then waits until client disconnects (cancel).
    let client_disconnected = Arc::new(AtomicBool::new(false));
    let disconnect_notify = Arc::new(Notify::new());
    let disc_flag = client_disconnected.clone();
    let disc_notify = disconnect_notify.clone();

    let (llama_url, llama_srv) = spawn_fake_llama(move |first_line, mut stream| {
        let disc_flag = disc_flag.clone();
        let disc_notify = disc_notify.clone();
        async move {
            if first_line.starts_with("POST /completion ") {
                let hdr = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n";
                let _ = stream.write_all(hdr.as_bytes()).await;
                let _ = stream
                    .write_all(b"data: {\"content\":\"first\",\"stop\":false}\n\n")
                    .await;
                let _ = stream.flush().await;
                // Wait for cancellation (client closing socket)
                let mut probe = [0u8; 16];
                let _ = tokio::time::timeout(Duration::from_secs(5), stream.read(&mut probe)).await;
                disc_flag.store(true, Ordering::SeqCst);
                disc_notify.notify_one();
            }
        }
    })
    .await;

    // 2. Fake coordinator HTTP + WS server handling challenge, token, and WS upgrade.
    let coord_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let coord_addr = coord_listener.local_addr().unwrap();
    let coord_task = tokio::spawn(async move {
        // Accept 3 connections: POST /api/node/auth/challenge, POST /api/node/auth/token, GET /api/node/ws
        for _ in 0..2 {
            let (mut s, _) = coord_listener.accept().await.unwrap();
            let mut buf = vec![0u8; 4096];
            let n = s.read(&mut buf).await.unwrap();
            let req = String::from_utf8_lossy(&buf[..n]);
            let body = if req.starts_with("POST /api/node/auth/challenge ") {
                r#"{"nonce":"test-nonce","expires_at":"2099-01-01T00:00:00Z"}"#
            } else {
                r#"{"access_token":"test-jwt","expires_at":"2099-01-01T00:00:00Z"}"#
            };
            let resp = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(),
                body
            );
            s.write_all(resp.as_bytes()).await.unwrap();
        }

        let (ws_tcp, _) = coord_listener.accept().await.unwrap();
        let mut ws = tokio_tungstenite::accept_async(ws_tcp).await.unwrap();
        let hello = ws.next().await.unwrap().unwrap();
        let hello_env: Envelope = serde_json::from_str(hello.to_text().unwrap()).unwrap();
        assert_eq!(hello_env.msg_type, "hello");

        let welcome = Envelope {
            v: 1,
            msg_type: "welcome".into(),
            message_id: uuid::Uuid::new_v4().to_string(),
            request_id: None,
            session_id: Some("sess-cancel".into()),
            seq: 1,
            sent_at: chrono::Utc::now().to_rfc3339(),
            payload: serde_json::json!({"session_id": "sess-cancel"}),
            error: None,
        };
        ws.send(Message::Text(serde_json::to_string(&welcome).unwrap()))
            .await
            .unwrap();

        let start = Envelope {
            v: 1,
            msg_type: "inference.start".into(),
            message_id: uuid::Uuid::new_v4().to_string(),
            request_id: Some("req-cancel-1".into()),
            session_id: Some("sess-cancel".into()),
            seq: 2,
            sent_at: chrono::Utc::now().to_rfc3339(),
            payload: serde_json::json!({
                "attempt_id": "att-cancel-1",
                "lease_id": "lease-cancel-1",
                "model_id": "llama-3-8b-instruct",
                "input_token_ids": [1, 2, 3],
                "tokenization_verified": true,
                "max_tokens": 32,
                "parameters": {"temperature": 0.7, "top_p": 1.0, "stop": []}
            }),
            error: None,
        };
        ws.send(Message::Text(serde_json::to_string(&start).unwrap()))
            .await
            .unwrap();

        // Wait for first delta, then send inference.cancel
        let mut got_delta = false;
        let mut got_cancelled = false;
        while let Some(Ok(Message::Text(txt))) = ws.next().await {
            let env: Envelope = serde_json::from_str(&txt).unwrap();
            if env.msg_type == "inference.delta" {
                got_delta = true;
                let cancel = Envelope {
                    v: 1,
                    msg_type: "inference.cancel".into(),
                    message_id: uuid::Uuid::new_v4().to_string(),
                    request_id: Some("req-cancel-1".into()),
                    session_id: Some("sess-cancel".into()),
                    seq: 3,
                    sent_at: chrono::Utc::now().to_rfc3339(),
                    payload: serde_json::json!({"attempt_id": "att-cancel-1"}),
                    error: None,
                };
                ws.send(Message::Text(serde_json::to_string(&cancel).unwrap()))
                    .await
                    .unwrap();
            } else if env.msg_type == "inference.cancelled" {
                got_cancelled = true;
                break;
            }
        }
        assert!(got_delta, "expected first delta before cancellation");
        assert!(got_cancelled, "expected inference.cancelled after cancel");
        let _ = ws.close(None).await;
    });

    let cfg = NodeConfig {
        server_origin: format!("http://{}", coord_addr),
        node_id: "node-cancel".into(),
        node_secret: "secret-cancel".into(),
        install_salt: "salt-cancel".into(),
    };
    let bk = Arc::new(LlamaServerBackend::connect_existing(
        llama_url,
        "llama-3-8b-instruct".into(),
    ));
    let http = reqwest::Client::new();
    let (_shut_tx, mut shut_rx) = tokio::sync::watch::channel(false);

    let session_res = tokio::time::timeout(
        Duration::from_secs(10),
        run_one_session(&http, &cfg, bk, &mut shut_rx),
    )
    .await
    .expect("ws session timed out");
    session_res.unwrap();
    coord_task.await.unwrap();

    // Confirm fake llama-server observed client disconnect
    if !client_disconnected.load(Ordering::SeqCst) {
        let _ = tokio::time::timeout(Duration::from_secs(2), disconnect_notify.notified()).await;
    }
    assert!(client_disconnected.load(Ordering::SeqCst));
    llama_srv.abort();
}

#[tokio::test]
async fn test_fake_llama_rejects_unverified_tokenization_without_calling_server() {
    let calls = Arc::new(AtomicUsize::new(0));
    let calls_cl = calls.clone();
    let (url, srv) = spawn_fake_llama(move |_first_line, mut stream| {
        let calls = calls_cl.clone();
        async move {
            calls.fetch_add(1, Ordering::SeqCst);
            let body = r#"{"content":"should never run","stop":true}"#;
            let resp = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(),
                body
            );
            let _ = stream.write_all(resp.as_bytes()).await;
        }
    })
    .await;

    let bk = LlamaServerBackend::connect_existing(url, "llama-3-8b-instruct".into());
    let mut req = sample_request();
    req.tokenization_verified = false;
    let (tx, mut rx) = mpsc::channel(16);
    bk.generate(req, tx).await.unwrap();

    match rx.recv().await.expect("expected error chunk") {
        BackendChunk::Error(msg) => {
            assert!(
                msg.contains("unverified"),
                "expected unverified tokenization error, got: {msg}"
            );
        }
        other => panic!("expected BackendChunk::Error, got {:?}", other),
    }
    assert_eq!(
        calls.load(Ordering::SeqCst),
        0,
        "fake llama-server must not be called when tokenization is unverified"
    );
    srv.abort();
}

#[tokio::test]
async fn test_fake_llama_truncated_sse_stream_returns_error() {
    let (url, srv) = spawn_fake_llama(move |first_line, mut stream| async move {
        if first_line.starts_with("POST /completion ") {
            let hdr =
                "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n";
            let _ = stream.write_all(hdr.as_bytes()).await;
            let _ = stream
                .write_all(b"data: {\"content\":\"partial\",\"stop\":false}\n\n")
                .await;
            let _ = stream.flush().await;
            // Close stream abruptly without stop=true terminal event
        }
    })
    .await;

    let bk = LlamaServerBackend::connect_existing(url, "llama-3-8b-instruct".into());
    let (tx, mut rx) = mpsc::channel(16);
    bk.generate(sample_request(), tx).await.unwrap();

    let mut deltas = Vec::new();
    let mut err_msg = None;
    while let Some(c) = rx.recv().await {
        match c {
            BackendChunk::Delta(d) => deltas.push(d),
            BackendChunk::Error(e) => {
                err_msg = Some(e);
                break;
            }
            BackendChunk::Done { .. } => {
                panic!("truncated SSE stream must not emit BackendChunk::Done");
            }
        }
    }
    assert_eq!(deltas, vec!["partial"]);
    let msg = err_msg.expect("expected BackendChunk::Error for truncated stream");
    assert!(
        msg.contains("prematurely") || msg.contains("stop=true"),
        "unexpected error: {msg}"
    );
    srv.abort();
}

#[tokio::test]
async fn test_fake_llama_malformed_sse_event_returns_error() {
    let (url, srv) = spawn_fake_llama(move |first_line, mut stream| async move {
        if first_line.starts_with("POST /completion ") {
            let hdr =
                "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n";
            let _ = stream.write_all(hdr.as_bytes()).await;
            let _ = stream.write_all(b"data: {not-valid-json\n\n").await;
            let _ = stream.flush().await;
        }
    })
    .await;

    let bk = LlamaServerBackend::connect_existing(url, "llama-3-8b-instruct".into());
    let (tx, mut rx) = mpsc::channel(16);
    bk.generate(sample_request(), tx).await.unwrap();

    match rx.recv().await.expect("expected error chunk") {
        BackendChunk::Error(msg) => {
            assert!(
                msg.contains("malformed SSE JSON"),
                "expected malformed SSE JSON error, got: {msg}"
            );
        }
        other => panic!("expected BackendChunk::Error, got {:?}", other),
    }
    srv.abort();
}

#[tokio::test]
async fn test_ws_rejects_inference_start_with_invalid_tokenization_verified_type() {
    let calls = Arc::new(AtomicUsize::new(0));
    let calls_srv = calls.clone();
    let (llama_url, llama_srv) = spawn_fake_llama(move |_first_line, mut stream| {
        let calls = calls_srv.clone();
        async move {
            calls.fetch_add(1, Ordering::SeqCst);
            let _ = stream
                .write_all(b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
                .await;
        }
    })
    .await;

    let ws_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let ws_addr = ws_listener.local_addr().unwrap();

    let server_task = tokio::spawn(async move {
        // 1. Challenge
        let (mut s1, _) = ws_listener.accept().await.unwrap();
        let mut buf = [0u8; 2048];
        let _ = s1.read(&mut buf).await;
        let body =
            r#"{"challenge_id":"c-bad","nonce":"n-bad","expires_at":"2099-01-01T00:00:00Z"}"#;
        let resp = format!(
            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
            body.len(),
            body
        );
        let _ = s1.write_all(resp.as_bytes()).await;

        // 2. Token
        let (mut s2, _) = ws_listener.accept().await.unwrap();
        let _ = s2.read(&mut buf).await;
        let body = r#"{"access_token":"tok-bad","expires_at":"2099-01-01T00:00:00Z"}"#;
        let resp = format!(
            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
            body.len(),
            body
        );
        let _ = s2.write_all(resp.as_bytes()).await;

        // 3. WS session
        let (s3, _) = ws_listener.accept().await.unwrap();
        let mut ws = tokio_tungstenite::accept_async(s3).await.unwrap();
        let hello = ws.next().await.unwrap().unwrap();
        let hello_env: Envelope = serde_json::from_str(hello.to_text().unwrap()).unwrap();
        assert_eq!(hello_env.msg_type, "hello");

        let welcome = Envelope {
            v: 1,
            msg_type: "welcome".into(),
            message_id: uuid::Uuid::new_v4().to_string(),
            request_id: None,
            session_id: Some("sess-bad-type".into()),
            seq: 1,
            sent_at: chrono::Utc::now().to_rfc3339(),
            payload: serde_json::json!({}),
            error: None,
        };
        ws.send(Message::Text(serde_json::to_string(&welcome).unwrap()))
            .await
            .unwrap();

        // Send inference.start with invalid non-boolean tokenization_verified ("true")
        let bad_start = Envelope {
            v: 1,
            msg_type: "inference.start".into(),
            message_id: uuid::Uuid::new_v4().to_string(),
            request_id: Some("req-bad-type".into()),
            session_id: Some("sess-bad-type".into()),
            seq: 2,
            sent_at: chrono::Utc::now().to_rfc3339(),
            payload: serde_json::json!({
                "attempt_id": "att-bad-type",
                "lease_id": "lease-bad-type",
                "model_id": "llama-3-8b-instruct",
                "input_token_ids": [1, 2, 3],
                "tokenization_verified": "true",
                "max_tokens": 16,
                "parameters": {"temperature": 0.5, "top_p": 1.0, "stop": []}
            }),
            error: None,
        };
        ws.send(Message::Text(serde_json::to_string(&bad_start).unwrap()))
            .await
            .unwrap();

        let reply = tokio::time::timeout(Duration::from_secs(3), ws.next())
            .await
            .expect("timed out waiting for error reply")
            .unwrap()
            .unwrap();
        let reply_env: Envelope = serde_json::from_str(reply.to_text().unwrap()).unwrap();
        assert_eq!(reply_env.msg_type, "error");
        let err = reply_env.error.expect("expected EnvelopeError");
        assert_eq!(err.code, "invalid_message");
        assert!(!err.retryable);
        assert_eq!(reply_env.payload["operation_type"], "inference.start");

        // Drain to close cleanly
        let drain = Envelope {
            v: 1,
            msg_type: "drain".into(),
            message_id: uuid::Uuid::new_v4().to_string(),
            request_id: None,
            session_id: Some("sess-bad-type".into()),
            seq: 3,
            sent_at: chrono::Utc::now().to_rfc3339(),
            payload: serde_json::json!({}),
            error: None,
        };
        let _ = ws
            .send(Message::Text(serde_json::to_string(&drain).unwrap()))
            .await;
    });

    let cfg = NodeConfig {
        server_origin: format!("http://{}", ws_addr),
        node_id: "node-bad-type".into(),
        node_secret: "secret-bad-type".into(),
        install_salt: "salt-bad-type".into(),
    };
    let bk = Arc::new(LlamaServerBackend::connect_existing(
        llama_url,
        "llama-3-8b-instruct".into(),
    ));
    let http = reqwest::Client::new();
    let (_tx, mut rx) = tokio::sync::watch::channel(false);

    run_one_session(&http, &cfg, bk, &mut rx).await.unwrap();
    server_task.await.unwrap();
    assert_eq!(
        calls.load(Ordering::SeqCst),
        0,
        "llama-server must not be called when inference.start has invalid tokenization_verified type"
    );
    llama_srv.abort();
}
