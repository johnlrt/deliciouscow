use anyhow::{Context, Result};
use rand::RngCore;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::fs;
use std::path::{Path, PathBuf};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NodeConfig {
    pub server_origin: String,
    pub node_id: String,
    pub node_secret: String,
    pub install_salt: String,
}

pub fn default_config_path() -> PathBuf {
    if let Ok(p) = std::env::var("CORDOVA_NODE_CONFIG") {
        return PathBuf::from(p);
    }
    let home = std::env::var("HOME").unwrap_or_else(|_| ".".to_string());
    PathBuf::from(home).join(".config/cordova-node/config.json")
}

pub fn load_config(path: &Path) -> Result<NodeConfig> {
    let raw = fs::read_to_string(path).with_context(|| format!("reading {}", path.display()))?;
    let cfg: NodeConfig = serde_json::from_str(&raw)?;
    Ok(cfg)
}

pub fn save_config(path: &Path, cfg: &NodeConfig) -> Result<()> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    let data = serde_json::to_string_pretty(cfg)?;
    let tmp = path.with_extension("json.tmp");
    fs::write(&tmp, data)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&tmp, fs::Permissions::from_mode(0o600))?;
    }
    fs::rename(tmp, path)?;
    Ok(())
}

pub fn generate_or_load_salt(path: &Path) -> String {
    if let Ok(c) = load_config(path) {
        if !c.install_salt.is_empty() {
            return c.install_salt;
        }
    }
    let mut buf = [0u8; 16];
    rand::thread_rng().fill_bytes(&mut buf);
    hex::encode(buf)
}

pub fn compute_hwid_hash(salt: &str) -> String {
    let host = std::env::var("HOSTNAME").unwrap_or_else(|_| "unknown-host".to_string());
    let input = format!(
        "cordova-hwid-v1:{}:{}:{}:{}",
        salt,
        std::env::consts::OS,
        std::env::consts::ARCH,
        host
    );
    let mut h = Sha256::new();
    h.update(input.as_bytes());
    hex::encode(h.finalize())
}
