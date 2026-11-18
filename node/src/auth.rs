use anyhow::{bail, Context, Result};
use hmac::{Hmac, Mac};
use reqwest::Client;
use serde::{Deserialize, Serialize};
use sha2::Sha256;
use std::time::Duration;

type HmacSha256 = Hmac<Sha256>;

#[derive(Debug, Deserialize)]
pub struct EnrollmentCreated {
    #[allow(dead_code)]
    pub enrollment_id: String,
    pub user_code: String,
    pub device_code: String,
    pub verification_uri: String,
    pub expires_at: String,
    pub poll_interval_seconds: u64,
}

#[derive(Debug, Deserialize)]
pub struct EnrollmentToken {
    pub node_id: String,
    pub node_secret: String,
    pub server_origin: String,
}

#[derive(Debug, Deserialize)]
struct ChallengeResp {
    nonce: String,
}

#[derive(Debug, Deserialize)]
struct TokenResp {
    access_token: String,
}

pub fn compute_proof(node_secret: &str, node_id: &str, nonce: &str) -> Result<String> {
    let msg = format!("cordova-node-auth-v1\n{}\n{}", node_id, nonce);
    let mut mac = HmacSha256::new_from_slice(node_secret.as_bytes())?;
    mac.update(msg.as_bytes());
    Ok(hex::encode(mac.finalize().into_bytes()))
}

pub async fn start_enrollment(
    client: &Client,
    server: &str,
    hwid_hash: &str,
    device_name: &str,
) -> Result<EnrollmentCreated> {
    #[derive(Serialize)]
    struct Req<'a> {
        hwid_hash: &'a str,
        node_version: &'a str,
        device_name: &'a str,
    }
    let url = format!("{}/api/node/enrollments", server.trim_end_matches('/'));
    let resp = client
        .post(&url)
        .json(&Req {
            hwid_hash,
            node_version: env!("CARGO_PKG_VERSION"),
            device_name,
        })
        .send()
        .await
        .context("enrollment request failed")?;
    if !resp.status().is_success() {
        bail!("enrollment failed with status {}", resp.status());
    }
    Ok(resp.json().await?)
}

pub async fn poll_enrollment_token(
    client: &Client,
    server: &str,
    device_code: &str,
    interval_secs: u64,
) -> Result<EnrollmentToken> {
    #[derive(Serialize)]
    struct Req<'a> {
        device_code: &'a str,
    }
    let url = format!(
        "{}/api/node/enrollments/token",
        server.trim_end_matches('/')
    );
    let interval = Duration::from_secs(interval_secs.max(1));
    loop {
        let resp = client.post(&url).json(&Req { device_code }).send().await?;
        if resp.status().as_u16() == 428 {
            tokio::time::sleep(interval).await;
            continue;
        }
        if resp.status().is_success() {
            return Ok(resp.json().await?);
        }
        bail!("enrollment polling failed: status {}", resp.status());
    }
}

pub async fn fetch_ws_access_token(
    client: &Client,
    server: &str,
    node_id: &str,
    node_secret: &str,
) -> Result<String> {
    let base = server.trim_end_matches('/');
    let chal_resp = client
        .post(format!("{}/api/node/auth/challenge", base))
        .json(&serde_json::json!({ "node_id": node_id }))
        .send()
        .await?;
    if !chal_resp.status().is_success() {
        bail!("challenge failed: {}", chal_resp.status());
    }
    let chal: ChallengeResp = chal_resp.json().await?;
    let proof = compute_proof(node_secret, node_id, &chal.nonce)?;
    let tok_resp = client
        .post(format!("{}/api/node/auth/token", base))
        .json(&serde_json::json!({
            "node_id": node_id,
            "nonce": chal.nonce,
            "proof": proof,
        }))
        .send()
        .await?;
    if !tok_resp.status().is_success() {
        bail!("auth token failed: {}", tok_resp.status());
    }
    let tok: TokenResp = tok_resp.json().await?;
    Ok(tok.access_token)
}
