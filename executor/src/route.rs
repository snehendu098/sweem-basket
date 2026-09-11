use alloy_primitives::Address;
use axum::{extract::State, http::StatusCode, Json};
use serde::{Deserialize, Serialize};
use std::str::FromStr;
use tracing::{info, warn};

use crate::{
    rpc::{self, Outcome, POLL_INTERVAL},
    venues::{approve_call, deposit_call, usd_to_units, withdraw_call, Call, Venue},
    AppState,
};

#[derive(Debug, Deserialize)]
pub struct RouteRequest {
    /// Wallet service's execution ID. Passed to Privy as `reference_id` so a
    /// retry after a network timeout is reconcilable rather than a double-spend.
    pub execution_id: String,
    pub user_wallet: String,
    /// Privy wallet ID for the user's embedded wallet — the delegation target.
    /// Defaulted, not required, so a caller that omits it gets a clear 400
    /// instead of an opaque 422 from serde.
    #[serde(default)]
    pub privy_wallet_id: String,
    pub asset: String,
    pub amount_usd: f64,
    /// deposit | withdraw | rebalance
    pub action: String,
    #[serde(default)]
    pub from_venue_id: String,
    #[serde(default)]
    pub to_venue_id: String,
}

/// One submitted call. A sequence that stops partway must be legible from the
/// response alone, so the wallet service records exactly how far it got.
#[derive(Debug, Serialize)]
pub struct StepResult {
    pub step: usize,
    pub tx_hash: String,
    /// confirmed | reverted | pending | submitted
    pub outcome: String,
}

#[derive(Debug, Serialize)]
pub struct RouteResponse {
    pub execution_id: String,
    /// Hash of the last call submitted. Per-call hashes are in `steps`.
    pub tx_hash: String,
    /// submitted | confirmed | pending | failed
    pub status: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub steps: Vec<StepResult>,
}

impl RouteResponse {
    fn failed(id: &str, msg: impl Into<String>) -> (StatusCode, Json<Self>) {
        (
            StatusCode::BAD_REQUEST,
            Json(Self {
                execution_id: id.into(),
                tx_hash: String::new(),
                status: "failed".into(),
                error: msg.into(),
                steps: Vec::new(),
            }),
        )
    }
}

pub async fn route(
    State(state): State<AppState>,
    Json(req): Json<RouteRequest>,
) -> Result<Json<RouteResponse>, (StatusCode, Json<RouteResponse>)> {
    if req.amount_usd <= 0.0 {
        return Err(RouteResponse::failed(&req.execution_id, "amount_usd must be positive"));
    }
    if req.privy_wallet_id.trim().is_empty() {
        return Err(RouteResponse::failed(&req.execution_id, "privy_wallet_id is required"));
    }

    let owner = Address::from_str(&req.user_wallet)
        .map_err(|_| RouteResponse::failed(&req.execution_id, "invalid user_wallet address"))?;

    // Resolve every referenced venue against the allowlist BEFORE building any
    // calldata. An unknown venue is refused outright — this is the security
    // boundary that stops the wallet service from directing funds anywhere.
    let from = lookup(&state, &req.from_venue_id, &req.execution_id)?;
    let to = lookup(&state, &req.to_venue_id, &req.execution_id)?;

    let calls: Vec<Call> = match req.action.as_str() {
        "deposit" => {
            let v = to.ok_or_else(|| {
                RouteResponse::failed(&req.execution_id, "deposit requires to_venue_id")
            })?;
            let amount = units(&req, v)?;
            vec![approve_call(v, amount), deposit_call(v, amount, owner)]
        }
        "withdraw" => {
            let v = from.ok_or_else(|| {
                RouteResponse::failed(&req.execution_id, "withdraw requires from_venue_id")
            })?;
            let amount = units(&req, v)?;
            vec![withdraw_call(v, amount, owner)]
        }
        "rebalance" => {
            let (src, dst) = match (from, to) {
                (Some(a), Some(b)) => (a, b),
                _ => {
                    return Err(RouteResponse::failed(
                        &req.execution_id,
                        "rebalance requires both from_venue_id and to_venue_id",
                    ))
                }
            };
            if src.chain_id != dst.chain_id {
                // Cross-chain needs a bridge leg and a second submission; refuse
                // rather than half-execute and strand funds mid-flight.
                return Err(RouteResponse::failed(
                    &req.execution_id,
                    "cross-chain rebalance not supported",
                ));
            }
            let amount = units(&req, src)?;
            vec![
                withdraw_call(src, amount, owner),
                approve_call(dst, amount),
                deposit_call(dst, amount, owner),
            ]
        }
        other => {
            return Err(RouteResponse::failed(
                &req.execution_id,
                format!("unknown action {other}"),
            ))
        }
    };

    let chain_id = to.or(from).map(|v| v.chain_id).unwrap_or_default();

    // Submit sequentially. Each call depends on the previous having landed
    // (approve before deposit, withdraw before redeposit), so this cannot be
    // parallelised.
    let mut last_hash = String::new();
    let mut steps: Vec<StepResult> = Vec::new();
    let total = calls.len();

    for (i, call) in calls.iter().enumerate() {
        // Privy caps reference_id at 64 chars; keep the suffix, it is what makes
        // each step of a multi-call execution distinct.
        let mut reference = format!("{}-{i}", req.execution_id);
        if reference.len() > 64 {
            reference = reference[reference.len() - 64..].to_string();
        }
        let hash = match state
            .privy
            .send_transaction(&req.privy_wallet_id, chain_id, call, &reference)
            .await
        {
            Ok(data) => {
                info!(execution_id = %req.execution_id, step = i, hash = %data.hash, "submitted");
                last_hash = data.hash.clone();
                data.hash
            }
            Err(e) => {
                warn!(execution_id = %req.execution_id, step = i, error = %e, "submit failed");
                steps.push(StepResult {
                    step: i,
                    tx_hash: String::new(),
                    outcome: "failed".into(),
                });
                return Err(finish(
                    StatusCode::BAD_GATEWAY,
                    &req.execution_id,
                    last_hash,
                    "failed",
                    format!("step {i}: {e}"),
                    steps,
                ));
            }
        };

        // Nothing depends on the last call, so it needs no confirmation.
        if i + 1 == total {
            steps.push(StepResult {
                step: i,
                tx_hash: hash,
                outcome: "submitted".into(),
            });
            break;
        }

        // Every later call depends on this one having landed: approve before
        // deposit, withdraw before redeposit. Submission is not inclusion.
        match rpc::wait(
            || state.rpc.receipt(&hash),
            state.receipt_timeout,
            POLL_INTERVAL,
        )
        .await
        {
            Some(Outcome::Confirmed) => steps.push(StepResult {
                step: i,
                tx_hash: hash,
                outcome: "confirmed".into(),
            }),
            Some(Outcome::Reverted) => {
                warn!(execution_id = %req.execution_id, step = i, hash = %hash, "reverted");
                steps.push(StepResult {
                    step: i,
                    tx_hash: hash,
                    outcome: "reverted".into(),
                });
                return Err(finish(
                    StatusCode::BAD_GATEWAY,
                    &req.execution_id,
                    last_hash,
                    "failed",
                    format!("step {i} reverted on chain"),
                    steps,
                ));
            }
            None => {
                // The transaction may still land. Calling this failed would
                // invite a duplicate retry, so report it pending and stop.
                warn!(execution_id = %req.execution_id, step = i, hash = %hash, "receipt timeout");
                steps.push(StepResult {
                    step: i,
                    tx_hash: hash,
                    outcome: "pending".into(),
                });
                return Ok(Json(RouteResponse {
                    execution_id: req.execution_id,
                    tx_hash: last_hash,
                    status: "pending".into(),
                    error: format!("step {i} not confirmed within the receipt timeout"),
                    steps,
                }));
            }
        }
    }

    Ok(Json(RouteResponse {
        execution_id: req.execution_id,
        tx_hash: last_hash,
        status: "submitted".into(),
        error: String::new(),
        steps,
    }))
}

fn finish(
    code: StatusCode,
    execution_id: &str,
    tx_hash: String,
    status: &str,
    error: String,
    steps: Vec<StepResult>,
) -> (StatusCode, Json<RouteResponse>) {
    (
        code,
        Json(RouteResponse {
            execution_id: execution_id.into(),
            tx_hash,
            status: status.into(),
            error,
            steps,
        }),
    )
}

fn lookup<'a>(
    state: &'a AppState,
    id: &str,
    execution_id: &str,
) -> Result<Option<&'a Venue>, (StatusCode, Json<RouteResponse>)> {
    if id.is_empty() {
        return Ok(None);
    }
    state
        .venues
        .get(id)
        .map(Some)
        .ok_or_else(|| RouteResponse::failed(execution_id, format!("venue not allowlisted: {id}")))
}

fn units(
    req: &RouteRequest,
    venue: &Venue,
) -> Result<alloy_primitives::U256, (StatusCode, Json<RouteResponse>)> {
    // usd_to_units is only correct for USD-pegged assets. Anything else needs a
    // price feed and a swap leg, which this executor does not do yet.
    if venue.symbol != req.asset {
        return Err(RouteResponse::failed(
            &req.execution_id,
            format!("venue {} holds {}, not {}", venue.id, venue.symbol, req.asset),
        ));
    }
    if !is_usd_pegged(&venue.symbol) {
        return Err(RouteResponse::failed(
            &req.execution_id,
            format!("non-stable asset {} needs a price feed; not supported yet", venue.symbol),
        ));
    }
    Ok(usd_to_units(req.amount_usd, venue.asset_decimals))
}

fn is_usd_pegged(symbol: &str) -> bool {
    matches!(symbol, "USDC" | "USDT" | "USDS" | "DAI" | "USDe" | "GHO")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{privy::Privy, venues::Registry};
    use std::sync::Arc;

    fn state() -> AppState {
        AppState {
            privy: Arc::new(Privy::new("app".into(), "secret".into(), None)),
            venues: Arc::new(Registry::load("venues.json").expect("venues.json must parse")),
            // Unreachable in these tests: every one is rejected before submission.
            rpc: Arc::new(crate::rpc::Rpc::new("http://127.0.0.1:1".into())),
            receipt_timeout: std::time::Duration::from_millis(1),
        }
    }

    fn req(action: &str) -> RouteRequest {
        RouteRequest {
            execution_id: "exec-1".into(),
            user_wallet: "0x1111111111111111111111111111111111111111".into(),
            privy_wallet_id: "wallet-1".into(),
            asset: "USDC".into(),
            amount_usd: 100.0,
            action: action.into(),
            from_venue_id: String::new(),
            to_venue_id: String::new(),
        }
    }

    /// Property 1: an id absent from the allowlist is refused before any
    /// calldata is built, so no network call is made and this test needs none.
    #[tokio::test]
    async fn unlisted_venue_is_refused() {
        let mut r = req("deposit");
        r.to_venue_id = "Base:evil:0xdeadbeef".into();
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_REQUEST);
        assert!(err.1.error.contains("not allowlisted"), "{}", err.1.error);
        assert_eq!(err.1.status, "failed");
    }

    /// Property 6: non-positive amounts never reach the signer.
    #[tokio::test]
    async fn non_positive_amount_is_refused() {
        for amount in [0.0, -1.0] {
            let mut r = req("deposit");
            r.amount_usd = amount;
            let err = route(State(state()), Json(r)).await.unwrap_err();
            assert_eq!(err.0, StatusCode::BAD_REQUEST);
            assert!(err.1.error.contains("positive"));
        }
    }

    #[tokio::test]
    async fn missing_wallet_id_is_refused() {
        let mut r = req("deposit");
        r.privy_wallet_id = String::new();
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert!(err.1.error.contains("privy_wallet_id"));
    }

    #[tokio::test]
    async fn unknown_action_is_refused() {
        let err = route(State(state()), Json(req("yolo"))).await.unwrap_err();
        assert!(err.1.error.contains("unknown action"));
    }

    /// The Go wallet client sends privy_did, chain and max_slippage_bps, which
    /// this handler ignores. Unknown fields must not fail the request.
    #[test]
    fn extra_wire_fields_are_ignored() {
        let body = serde_json::json!({
            "execution_id": "e", "user_wallet": "0x1", "privy_wallet_id": "w",
            "privy_did": "did:privy:x", "chain": "Base", "asset": "USDC",
            "amount_usd": 10.0, "action": "deposit", "max_slippage_bps": 50
        });
        let parsed: RouteRequest = serde_json::from_value(body).expect("must ignore extras");
        assert_eq!(parsed.action, "deposit");
        assert!(parsed.from_venue_id.is_empty());
    }

    #[test]
    fn usd_pegged_recognises_stables_only() {
        assert!(is_usd_pegged("USDC"));
        assert!(is_usd_pegged("GHO"));
        assert!(!is_usd_pegged("WETH"));
        assert!(!is_usd_pegged("cbBTC"));
    }

    /// A partial sequence must be legible from the response alone: which step
    /// reverted, and the hash of every call that was submitted.
    #[test]
    fn partial_sequence_is_legible_from_the_response() {
        let r = RouteResponse {
            execution_id: "e".into(),
            tx_hash: "0xbb".into(),
            status: "failed".into(),
            error: "step 1 reverted on chain".into(),
            steps: vec![
                StepResult { step: 0, tx_hash: "0xaa".into(), outcome: "confirmed".into() },
                StepResult { step: 1, tx_hash: "0xbb".into(), outcome: "reverted".into() },
            ],
        };
        let v = serde_json::to_value(&r).unwrap();
        assert_eq!(v["status"], "failed");
        assert_eq!(v["steps"][0]["outcome"], "confirmed");
        assert_eq!(v["steps"][1]["outcome"], "reverted");
        assert_eq!(v["steps"][1]["tx_hash"], "0xbb");
        assert_eq!(v["steps"].as_array().unwrap().len(), 2, "the third call was never submitted");
    }

    /// Rejections happen before submission, so they carry no steps and the Go
    /// client sees a clean 400.
    #[test]
    fn rejection_carries_no_steps() {
        let (code, Json(r)) = RouteResponse::failed("e", "nope");
        assert_eq!(code, StatusCode::BAD_REQUEST);
        assert!(r.steps.is_empty());
        assert!(serde_json::to_value(&r).unwrap().get("steps").is_none());
    }
}
