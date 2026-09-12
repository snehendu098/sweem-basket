use std::{collections::HashMap, env, time::Duration};

pub const CHAIN_IDS: [u64; 2] = [8453, 84532];

pub struct Config {
    pub addr: String,
    pub privy_app_id: String,
    pub privy_app_secret: String,
    pub privy_authorization_key: Option<String>,
    pub venues_path: String,
    pub swaps_path: String,
    pub rpc_urls: HashMap<u64, String>,
    pub receipt_timeout: Duration,
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
            swaps_path: env::var("SWAPS_PATH").unwrap_or_else(|_| "swaps.json".into()),
            rpc_urls: CHAIN_IDS.iter().map(|&id| (id, rpc_url(id))).collect(),
            receipt_timeout: Duration::from_secs(
                env::var("RECEIPT_TIMEOUT")
                    .ok()
                    .and_then(|v| v.trim().parse().ok())
                    .unwrap_or(60),
            ),
        })
    }
}

fn rpc_url(chain_id: u64) -> String {
    let default = match chain_id {
        84532 => "https://sepolia.base.org",
        _ => "https://mainnet.base.org",
    };
    env::var(format!("BASE_RPC_URL_{chain_id}"))
        .ok()
        .filter(|v| !v.trim().is_empty())
        .unwrap_or_else(|| default.into())
}

fn require(key: &str) -> Result<String, String> {
    match env::var(key) {
        Ok(v) if !v.trim().is_empty() => Ok(v),
        _ => Err(format!("missing required env var {key}")),
    }
}

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
