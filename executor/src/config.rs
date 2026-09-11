use std::{env, time::Duration};

/// Runtime configuration. Every field is required except `addr` — the executor
/// signs transactions, so it refuses to start half-configured rather than
/// failing at the first request.
#[derive(Clone)]
pub struct Config {
    pub addr: String,
    pub privy_app_id: String,
    pub privy_app_secret: String,
    /// `wallet-auth:<base64 PKCS#8 P-256 key>`. Optional: wallets that are not
    /// owned by an authorization key or key quorum do not need it, so a missing
    /// key warns instead of refusing to start.
    pub privy_authorization_key: Option<String>,
    /// Path to the venue allowlist. This file is the security boundary: the
    /// executor will only ever build calldata for venues listed here.
    pub venues_path: String,
    /// Read-only JSON-RPC node, used to wait for receipts between the calls of
    /// a sequence. Privy's wallet API only signs; it cannot read receipts.
    pub rpc_url: String,
    /// How long to wait for a receipt before reporting the step as pending.
    pub receipt_timeout: Duration,
}

/// Hand-written so credentials never reach a log line.
impl std::fmt::Debug for Config {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Config")
            .field("addr", &self.addr)
            .field("privy_app_id", &self.privy_app_id)
            .field("privy_app_secret", &"<redacted>")
            .field(
                "privy_authorization_key",
                &self.privy_authorization_key.as_ref().map(|_| "<redacted>"),
            )
            .field("venues_path", &self.venues_path)
            .field("rpc_url", &self.rpc_url)
            .field("receipt_timeout", &self.receipt_timeout)
            .finish()
    }
}

impl Config {
    pub fn from_env() -> Result<Self, String> {
        load_dotenv(".env");
        load_dotenv("../.env");
        Ok(Self {
            addr: env::var("EXECUTOR_ADDR").unwrap_or_else(|_| "0.0.0.0:8082".into()),
            privy_app_id: require("PRIVY_APP_ID")?,
            privy_app_secret: require("PRIVY_APP_SECRET")?,
            privy_authorization_key: env::var("PRIVY_AUTHORIZATION_PRIVATE_KEY")
                .ok()
                .filter(|v| !v.trim().is_empty()),
            venues_path: env::var("VENUES_PATH").unwrap_or_else(|_| "venues.json".into()),
            // Base Sepolia is the deployment target; the public endpoint
            // rate-limits and caps eth_getLogs at 10k blocks.
            rpc_url: env::var("BASE_RPC_URL")
                .unwrap_or_else(|_| "https://sepolia.base.org".into()),
            receipt_timeout: Duration::from_secs(
                env::var("RECEIPT_TIMEOUT")
                    .ok()
                    .and_then(|v| v.trim().parse().ok())
                    .unwrap_or(60),
            ),
        })
    }
}

fn require(key: &str) -> Result<String, String> {
    match env::var(key) {
        Ok(v) if !v.trim().is_empty() => Ok(v),
        _ => Err(format!("missing required env var {key}")),
    }
}

/// Minimal .env reader. Real environment variables always win.
fn load_dotenv(path: &str) {
    let Ok(body) = std::fs::read_to_string(path) else {
        return;
    };
    for line in body.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if let Some((k, v)) = line.split_once('=') {
            if env::var(k.trim()).is_err() {
                unsafe { env::set_var(k.trim(), v.trim()) };
            }
        }
    }
}
