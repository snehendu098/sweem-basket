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
    pub async fn receipt(&self, hash: &str) -> Option<Outcome> {
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
        let status = v.get("result")?.get("status")?.as_str()?;
        Some(if status == "0x1" {
            Outcome::Confirmed
        } else {
            Outcome::Reverted
        })
    }
}

/// Poll until the transaction is mined or `timeout` elapses.
///
/// `None` on timeout is NOT failure: the transaction may still land, and telling
/// the caller it failed would invite a duplicate retry. `fetch` is a closure so
/// this can be tested without a node.
pub async fn wait<F, Fut>(mut fetch: F, timeout: Duration, interval: Duration) -> Option<Outcome>
where
    F: FnMut() -> Fut,
    Fut: Future<Output = Option<Outcome>>,
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
        let got = wait(|| async { None }, Duration::from_millis(20), FAST).await;
        assert_eq!(got, None, "timeout must be distinguishable from a revert");
    }
}
