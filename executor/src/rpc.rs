//! Read-only JSON-RPC against the chain, used only to wait for receipts.
//!
//! Privy's wallet API is a *signing* API: `/v1/wallets/{id}/rpc` is scoped to a
//! wallet and dispatches signing methods. A receipt lookup is not wallet-scoped
//! and is not among them, so confirmation has to come from an ordinary node.

use serde_json::json;
use std::{future::Future, time::Duration};

/// Interval between receipt polls. Base blocks are ~2s.
pub const POLL_INTERVAL: Duration = Duration::from_secs(1);

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Outcome {
    Confirmed,
    Reverted,
}

/// A mined transaction: its status, plus the events it emitted.
///
/// The logs matter because Compound-v2 forks (Moonwell's mTokens) report some
/// failures as a RETURN VALUE, not a revert — the transaction still succeeds.
/// Status alone would read that as a confirmed deposit of money that never
/// moved, so those venues are verified by the event they emit on success.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Receipt {
    pub outcome: Outcome,
    /// (emitting contract, topic0), both lowercase hex.
    pub logs: Vec<(String, String)>,
}

impl Receipt {
    /// Whether `topic` was emitted by `address`.
    pub fn emitted(&self, address: &str, topic: &str) -> bool {
        let (address, topic) = (address.to_lowercase(), topic.to_lowercase());
        self.logs
            .iter()
            .any(|(a, t)| *a == address && *t == topic)
    }
}

#[derive(Clone)]
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

    /// `None` means "no receipt yet" — which also covers a transport blip. The
    /// two are deliberately not distinguished: both mean keep waiting, and a
    /// node we cannot read is not evidence a transaction failed.
    pub async fn receipt(&self, hash: &str) -> Option<Receipt> {
        let body = json!({
            "jsonrpc": "2.0", "id": 1,
            "method": "eth_getTransactionReceipt", "params": [hash]
        });
        let v: serde_json::Value = self
            .http
            .post(&self.url)
            .json(&body)
            .send()
            .await
            .ok()?
            .json()
            .await
            .ok()?;
        let result = v.get("result")?;
        let status = result.get("status")?.as_str()?;
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
            outcome: if status == "0x1" {
                Outcome::Confirmed
            } else {
                Outcome::Reverted
            },
            logs,
        })
    }
}

impl Rpc {
    /// Read-only `eth_call`. Used to quote a swap before submitting it: the
    /// quote is what bounds the trade, so `None` (a node we cannot reach, or a
    /// reverting call) must fail the leg rather than default to no bound.
    pub async fn eth_call(&self, to: &str, data: &[u8]) -> Option<Vec<u8>> {
        let body = json!({
            "jsonrpc": "2.0", "id": 1, "method": "eth_call",
            "params": [{ "to": to, "data": format!("0x{}", alloy_primitives::hex::encode(data)) }, "latest"]
        });
        let v: serde_json::Value = self
            .http
            .post(&self.url)
            .json(&body)
            .send()
            .await
            .ok()?
            .json()
            .await
            .ok()?;
        // An error object means the call reverted. Not a quote either way.
        let result = v.get("result")?.as_str()?;
        alloy_primitives::hex::decode(result).ok()
    }
}

/// Poll until the transaction is mined or `timeout` elapses.
///
/// `None` on timeout is NOT failure: the transaction may still land, and telling
/// the caller it failed would invite a duplicate retry. `fetch` is a closure so
/// this can be tested without a node.
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
                    // Not mined for the first two polls.
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

    /// The whole point of carrying logs: a Compound-fork failure is a
    /// successful transaction that simply did not emit its success event.
    #[test]
    fn emitted_matches_address_and_topic_case_insensitively() {
        let r = Receipt {
            outcome: Outcome::Confirmed,
            logs: vec![("0xAAaa".to_lowercase(), "0xBBbb".to_lowercase())],
        };
        assert!(r.emitted("0xAAAA", "0xBBBB"));
        assert!(!r.emitted("0xAAAA", "0xcccc"), "wrong topic must not match");
        assert!(!r.emitted("0xdddd", "0xBBBB"), "wrong emitter must not match");
    }
}
