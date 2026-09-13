use serde_json::json;
use std::{future::Future, time::Duration};

pub const POLL_INTERVAL: Duration = Duration::from_secs(1);

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Outcome {
    Confirmed,
    Reverted,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Receipt {
    pub outcome: Outcome,
    pub block: u64,
    pub logs: Vec<(String, String)>,
}

impl Receipt {
    pub fn emitted(&self, address: &str, topic: &str) -> bool {
        let (address, topic) = (address.to_lowercase(), topic.to_lowercase());
        self.logs
            .iter()
            .any(|(a, t)| *a == address && *t == topic)
    }
}

pub struct Rpc {
    http: reqwest::Client,
    url: String,
}

impl Rpc {
    pub fn new(url: String) -> Self {
        Self {
            http: reqwest::Client::new(),
            url,
        }
    }

    async fn post(&self, body: serde_json::Value) -> Option<serde_json::Value> {
        self.http.post(&self.url).json(&body).send().await.ok()?.json().await.ok()
    }

    pub async fn receipt(&self, hash: &str) -> Option<Receipt> {
        let body = json!({
            "jsonrpc": "2.0", "id": 1,
            "method": "eth_getTransactionReceipt", "params": [hash]
        });
        let v = self.post(body).await?;
        let result = v.get("result")?;
        let status = result.get("status")?.as_str()?;
        let block = result
            .get("blockNumber")
            .and_then(|b| b.as_str())
            .and_then(|b| u64::from_str_radix(b.trim_start_matches("0x"), 16).ok())
            .unwrap_or(0);
        let logs = result
            .get("logs")
            .and_then(|l| l.as_array())
            .map(|entries| {
                entries
                    .iter()
                    .filter_map(|e| {
                        let address = e.get("address")?.as_str()?.to_lowercase();
                        let topic0 = e.get("topics")?.as_array()?.first()?.as_str()?.to_lowercase();
                        Some((address, topic0))
                    })
                    .collect()
            })
            .unwrap_or_default();
        Some(Receipt {
            block,
            outcome: if status == "0x1" {
                Outcome::Confirmed
            } else {
                Outcome::Reverted
            },
            logs,
        })
    }

    pub async fn block_number(&self) -> Option<u64> {
        let v = self
            .post(json!({"jsonrpc": "2.0", "id": 1, "method": "eth_blockNumber", "params": []}))
            .await?;
        u64::from_str_radix(v.get("result")?.as_str()?.trim_start_matches("0x"), 16).ok()
    }

    pub async fn eth_call(&self, to: &str, data: &[u8]) -> Option<Vec<u8>> {
        let body = json!({
            "jsonrpc": "2.0", "id": 1, "method": "eth_call",
            "params": [{ "to": to, "data": format!("0x{}", alloy_primitives::hex::encode(data)) }, "latest"]
        });
        let v = self.post(body).await?;
        let result = v.get("result")?.as_str()?;
        alloy_primitives::hex::decode(result).ok()
    }
}

pub async fn wait<T, F, Fut>(mut fetch: F, timeout: Duration, interval: Duration) -> Option<T>
where
    F: FnMut() -> Fut,
    Fut: Future<Output = Option<T>>,
{
    let deadline = tokio::time::Instant::now() + timeout;
    loop {
        if let Some(o) = fetch().await {
            return Some(o);
        }
        if tokio::time::Instant::now() + interval >= deadline {
            return None;
        }
        tokio::time::sleep(interval).await;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::Cell;

    const FAST: Duration = Duration::from_millis(1);

    #[tokio::test]
    async fn returns_the_outcome_once_mined() {
        let calls = Cell::new(0);
        let got = wait(
            || {
                calls.set(calls.get() + 1);
                let n = calls.get();
                async move {
                    (n >= 3).then_some(Outcome::Confirmed)
                }
            },
            Duration::from_millis(200),
            FAST,
        )
        .await;
        assert_eq!(got, Some(Outcome::Confirmed));
        assert_eq!(calls.get(), 3);
    }

    #[tokio::test]
    async fn surfaces_a_revert() {
        let got = wait(
            || async { Some(Outcome::Reverted) },
            Duration::from_millis(200),
            FAST,
        )
        .await;
        assert_eq!(got, Some(Outcome::Reverted));
    }

    #[tokio::test]
    async fn timeout_is_none_not_failure() {
        let got: Option<Outcome> = wait(|| async { None }, Duration::from_millis(20), FAST).await;
        assert_eq!(got, None, "timeout must be distinguishable from a revert");
    }

    #[test]
    fn emitted_matches_address_and_topic_case_insensitively() {
        let r = Receipt {
            block: 1,
            outcome: Outcome::Confirmed,
            logs: vec![("0xAAaa".to_lowercase(), "0xBBbb".to_lowercase())],
        };
        assert!(r.emitted("0xAAAA", "0xBBBB"));
        assert!(!r.emitted("0xAAAA", "0xcccc"), "wrong topic must not match");
        assert!(!r.emitted("0xdddd", "0xBBBB"), "wrong emitter must not match");
    }
}
