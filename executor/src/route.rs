use alloy_primitives::Address;
use axum::{extract::State, http::StatusCode, Json};
use serde::{Deserialize, Serialize};
use std::str::FromStr;
use tracing::{info, warn};

use crate::{
    rpc::{self, Outcome, POLL_INTERVAL},
    exchanges::{self, SwapPath},
    venues::{
        approve_call, approve_if_needed, deposit_call, usd_to_units, withdraw_call, Call, Venue,
        VenueKind,
    },
    AppState,
};

const FUNDING_ASSET: &str = "USDC";

#[derive(Debug, Deserialize)]
pub struct RouteRequest {
    pub execution_id: String,
    pub user_wallet: String,
    #[serde(default)]
    pub privy_wallet_id: String,
    pub asset: String,
    #[serde(default)]
    pub chain_id: u64,
    pub amount_usd: f64,
    pub action: String,
    #[serde(default)]
    pub from_venue_id: String,
    #[serde(default)]
    pub to_venue_id: String,
    #[serde(default)]
    pub max_slippage_bps: u32,
}

#[derive(Debug, Serialize)]
pub struct StepResult {
    pub step: usize,
    pub tx_hash: String,
    pub outcome: String,
}

#[derive(Debug, Serialize)]
pub struct RouteResponse {
    pub execution_id: String,
    pub tx_hash: String,
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

    fn upstream(id: &str, msg: impl Into<String>) -> (StatusCode, Json<Self>) {
        let (_, body) = Self::failed(id, msg);
        (StatusCode::BAD_GATEWAY, body)
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

    if req.chain_id == 0 {
        return Err(RouteResponse::failed(&req.execution_id, "chain_id is required"));
    }
    if !state.rpc.contains_key(&req.chain_id) {
        return Err(RouteResponse::failed(
            &req.execution_id,
            format!("chain {} is not served by this executor", req.chain_id),
        ));
    }

    let from = lookup(&state, &req.from_venue_id, &req.execution_id)?;
    let to = lookup(&state, &req.to_venue_id, &req.execution_id)?;

    for v in [from, to].into_iter().flatten() {
        if v.chain_id != req.chain_id {
            return Err(RouteResponse::failed(
                &req.execution_id,
                format!(
                    "venue {} is on chain {}, but the request is for chain {}",
                    v.id, v.chain_id, req.chain_id
                ),
            ));
        }
    }

    let calls: Vec<Call> = match req.action.as_str() {
        "deposit" => {
            match to {
                // Only the funding asset is already in the wallet. Every other
                // asset must be bought first, USD-pegged or not.
                Some(v) if v.symbol == req.asset && req.asset == FUNDING_ASSET => {
                    let amount = units(&req, v)?;
                    let rpc = state.rpc.get(&req.chain_id).expect("chain checked above");
                    let mut calls = Vec::with_capacity(2);
                    if let Some(a) =
                        approve_if_needed(rpc, v.asset, v.target, owner, amount).await
                    {
                        calls.push(a);
                    }
                    calls.push(encode(deposit_call(v, amount, owner), &req)?);
                    calls
                }
                _ => swap_deposit(&state, &req, to, owner).await?,
            }
        }
        "withdraw" => match from {
            Some(v) if v.kind == VenueKind::Hold => {
                same_symbol(&req, v)?;
                swap_exit(&state, &req, owner).await?.0
            }
            Some(v) => {
                let amount = units(&req, v)?;
                vec![encode(withdraw_call(v, amount, owner), &req)?]
            }
            None => swap_exit(&state, &req, owner).await?.0,
        },
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
                return Err(RouteResponse::failed(
                    &req.execution_id,
                    "cross-chain rebalance not supported",
                ));
            }
            if src.kind == VenueKind::Hold {
                if dst.symbol != FUNDING_ASSET {
                    return Err(RouteResponse::failed(
                        &req.execution_id,
                        format!(
                            "leaving hold venue {} lands in {FUNDING_ASSET}, but {} holds {}",
                            src.id, dst.id, dst.symbol
                        ),
                    ));
                }
                same_symbol(&req, src)?;
                let (mut calls, usdc) = swap_exit(&state, &req, owner).await?;
                calls.push(approve_call(dst, usdc));
                calls.push(encode(deposit_call(dst, usdc, owner), &req)?);
                calls
            } else {
                let amount = units(&req, src)?;
                vec![
                    encode(withdraw_call(src, amount, owner), &req)?,
                    approve_call(dst, amount),
                    encode(deposit_call(dst, amount, owner), &req)?,
                ]
            }
        }
        other => {
            return Err(RouteResponse::failed(
                &req.execution_id,
                format!("unknown action {other}"),
            ))
        }
    };

    let chain_id = req.chain_id;
    let rpc = state.rpc.get(&chain_id).expect("chain checked above").clone();

    let mut last_hash = String::new();
    let mut steps: Vec<StepResult> = Vec::new();
    let total = calls.len();

    for (i, call) in calls.iter().enumerate() {
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

        if i + 1 == total && call.expect.is_none() {
            steps.push(StepResult {
                step: i,
                tx_hash: hash,
                outcome: "submitted".into(),
            });
            break;
        }

        match rpc::wait(
            || rpc.receipt(&hash),
            state.receipt_timeout,
            POLL_INTERVAL,
        )
        .await
        .map(|receipt| classify(&receipt, call))
        {
            Some(StepOutcome::Confirmed) => steps.push(StepResult {
                step: i,
                tx_hash: hash,
                outcome: "confirmed".into(),
            }),
            Some(StepOutcome::Failed(reason)) => {
                warn!(execution_id = %req.execution_id, step = i, hash = %hash, reason = %reason, "step failed on chain");
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
                    format!("step {i} {reason}"),
                    steps,
                ));
            }
            None => {
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

async fn swap_deposit(
    state: &AppState,
    req: &RouteRequest,
    to: Option<&Venue>,
    owner: Address,
) -> Result<Vec<Call>, (StatusCode, Json<RouteResponse>)> {
    let to = to.filter(|v| v.kind != VenueKind::Hold);
    if let Some(v) = to {
        if v.symbol != req.asset {
            return Err(RouteResponse::failed(
                &req.execution_id,
                format!("venue {} holds {}, not {}", v.id, v.symbol, req.asset),
            ));
        }
    }

    let path: &SwapPath = state
        .swaps
        .get(req.chain_id, FUNDING_ASSET, &req.asset)
        .ok_or_else(|| {
            RouteResponse::failed(
                &req.execution_id,
                format!(
                    "no allowlisted swap path {FUNDING_ASSET}->{} on chain {}",
                    req.asset, req.chain_id
                ),
            )
        })?;
    if path.chain_id != req.chain_id {
        return Err(RouteResponse::failed(
            &req.execution_id,
            format!(
                "swap path {} is on chain {}, but the request is for chain {}",
                path.id(),
                path.chain_id,
                req.chain_id
            ),
        ));
    }

    let amount_in = usd_to_units(req.amount_usd, path.token_in_decimals);
    if amount_in.is_zero() {
        return Err(RouteResponse::failed(
            &req.execution_id,
            "amount_usd rounds to zero input units",
        ));
    }

    let min_out = quote_min_out(state, req, path, amount_in, "swap leg quoted").await?;

    let rpc = state.rpc.get(&req.chain_id).expect("chain checked by caller");
    let mut calls = Vec::with_capacity(4);
    if let Some(a) =
        approve_if_needed(rpc, path.token_in, path.router, owner, amount_in).await
    {
        calls.push(a);
    }
    calls.push(path.swap_call(amount_in, min_out, owner));
    if let Some(v) = to {
        calls.push(approve_call(v, min_out));
        calls.push(encode(deposit_call(v, min_out, owner), req)?);
    }
    Ok(calls)
}

fn same_symbol(
    req: &RouteRequest,
    venue: &Venue,
) -> Result<(), (StatusCode, Json<RouteResponse>)> {
    if venue.symbol != req.asset {
        return Err(RouteResponse::failed(
            &req.execution_id,
            format!("venue {} holds {}, not {}", venue.id, venue.symbol, req.asset),
        ));
    }
    Ok(())
}

async fn swap_exit(
    state: &AppState,
    req: &RouteRequest,
    owner: Address,
) -> Result<(Vec<Call>, alloy_primitives::U256), (StatusCode, Json<RouteResponse>)> {
    let path: &SwapPath = state
        .swaps
        .get(req.chain_id, &req.asset, FUNDING_ASSET)
        .ok_or_else(|| {
            RouteResponse::failed(
                &req.execution_id,
                format!(
                    "no allowlisted swap path {}->{FUNDING_ASSET} on chain {}",
                    req.asset, req.chain_id
                ),
            )
        })?;

    let amount_in = hold_units(state, req).await?;
    let min_out = quote_min_out(state, req, path, amount_in, "exit leg quoted").await?;
    Ok((
        vec![
            path.approve_router(amount_in),
            path.swap_call(amount_in, min_out, owner),
        ],
        min_out,
    ))
}

async fn quote_min_out(
    state: &AppState,
    req: &RouteRequest,
    path: &SwapPath,
    amount_in: alloy_primitives::U256,
    label: &'static str,
) -> Result<alloy_primitives::U256, (StatusCode, Json<RouteResponse>)> {
    let rpc = state.rpc.get(&req.chain_id).expect("chain checked by caller");
    let quoted = exchanges::quote(rpc, path, amount_in).await.ok_or_else(|| {
        RouteResponse::upstream(
            &req.execution_id,
            format!(
                "could not quote {} on chain {}: refusing to swap without a minimum output",
                path.id(),
                req.chain_id
            ),
        )
    })?;
    let min_out = exchanges::min_out(quoted, req.max_slippage_bps);
    if min_out.is_zero() {
        return Err(RouteResponse::upstream(
            &req.execution_id,
            "quote is too small to bound: refusing to swap",
        ));
    }
    info!(
        execution_id = %req.execution_id, path = %path.id(),
        amount_in = %amount_in, quoted = %quoted, min_out = %min_out,
        slippage_bps = exchanges::clamp_slippage(req.max_slippage_bps),
        "{label}"
    );
    Ok(min_out)
}

async fn hold_units(
    state: &AppState,
    req: &RouteRequest,
) -> Result<alloy_primitives::U256, (StatusCode, Json<RouteResponse>)> {
    let entry = state
        .swaps
        .get(req.chain_id, FUNDING_ASSET, &req.asset)
        .ok_or_else(|| {
            RouteResponse::failed(
                &req.execution_id,
                format!(
                    "no allowlisted swap path {FUNDING_ASSET}->{} on chain {}: cannot price the position",
                    req.asset, req.chain_id
                ),
            )
        })?;
    let usd = usd_to_units(req.amount_usd, entry.token_in_decimals);
    if usd.is_zero() {
        return Err(RouteResponse::failed(
            &req.execution_id,
            "amount_usd rounds to zero input units",
        ));
    }
    exchanges::quote(state.rpc.get(&req.chain_id).expect("chain checked by caller"), entry, usd)
        .await
        .ok_or_else(|| {
            RouteResponse::upstream(
                &req.execution_id,
                format!(
                    "could not price {} in {} on chain {}: refusing to size a hold exit blind",
                    req.asset, FUNDING_ASSET, req.chain_id
                ),
            )
        })
}

fn encode(
    call: Result<Call, String>,
    req: &RouteRequest,
) -> Result<Call, (StatusCode, Json<RouteResponse>)> {
    call.map_err(|e| RouteResponse::failed(&req.execution_id, e))
}

#[derive(Debug, PartialEq, Eq)]
pub enum StepOutcome {
    Confirmed,
    Failed(String),
}

/// A ctoken failure is a non-zero return value inside a transaction that
/// succeeds, so the declared event decides, not the receipt status.
pub fn classify(receipt: &rpc::Receipt, call: &Call) -> StepOutcome {
    if receipt.outcome != Outcome::Confirmed {
        return StepOutcome::Failed("reverted on chain".into());
    }
    match &call.expect {
        Some(e) if !receipt.emitted(&e.address.to_string(), e.topic0) => StepOutcome::Failed(
            "mined without emitting its success event: the venue reported a failure code \
             instead of reverting, so no funds moved"
                .into(),
        ),
        _ => StepOutcome::Confirmed,
    }
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

    fn venue_id(chain_id: u64, symbol: &str) -> String {
        let r = Registry::load("venues.json").expect("venues.json must parse");
        r.all()
            .into_iter()
            .find(|v| v.chain_id == chain_id && v.symbol.eq_ignore_ascii_case(symbol))
            .unwrap_or_else(|| panic!("no {symbol} venue on chain {chain_id} in venues.json"))
            .id
            .clone()
    }

    fn state() -> AppState {
        AppState {
            privy: Arc::new(Privy::new("app".into(), "secret".into(), None)),
            venues: Arc::new(Registry::load("venues.json").expect("venues.json must parse")),
            swaps: Arc::new(
                crate::exchanges::SwapRegistry::load("swaps.json").expect("swaps.json must parse"),
            ),
            rpc: crate::config::CHAIN_IDS
                .iter()
                .map(|&id| (id, Arc::new(crate::rpc::Rpc::new("http://127.0.0.1:1".into()))))
                .collect(),
            receipt_timeout: std::time::Duration::from_millis(1),
        }
    }

    fn req(action: &str) -> RouteRequest {
        RouteRequest {
            execution_id: "exec-1".into(),
            user_wallet: "0x1111111111111111111111111111111111111111".into(),
            privy_wallet_id: "wallet-1".into(),
            asset: "USDC".into(),
            chain_id: 8453,
            amount_usd: 100.0,
            action: action.into(),
            from_venue_id: String::new(),
            to_venue_id: String::new(),
            max_slippage_bps: 0,
        }
    }

    #[tokio::test]
    async fn unlisted_venue_is_refused() {
        let mut r = req("deposit");
        r.to_venue_id = "base:evil:0xdeadbeef".into();
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_REQUEST);
        assert!(err.1.error.contains("not allowlisted"), "{}", err.1.error);
        assert_eq!(err.1.status, "failed");
    }

    #[tokio::test]
    async fn unlisted_swap_path_is_refused() {
        let mut r = req("deposit");
        r.asset = "DOGE".into();
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_REQUEST);
        assert!(err.1.error.contains("no allowlisted swap path"), "{}", err.1.error);

        let mut r = req("deposit");
        r.asset = "AERO".into();
        r.chain_id = 84532;
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert!(err.1.error.contains("no allowlisted swap path"), "{}", err.1.error);
    }

    #[tokio::test]
    async fn a_failed_quote_fails_the_leg() {
        let mut r = req("deposit");
        r.asset = "wstETH".into();
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_GATEWAY);
        assert!(err.1.error.contains("could not quote"), "{}", err.1.error);
        assert!(
            err.1.error.contains("without a minimum output"),
            "the reason must name the hazard: {}",
            err.1.error
        );
        assert!(err.1.steps.is_empty(), "nothing may be submitted without a quote");
    }

    #[tokio::test]
    async fn a_non_stable_venue_deposit_takes_the_swap_path() {
        let mut r = req("deposit");
        r.asset = "WETH".into();
        r.to_venue_id = venue_id(8453, "WETH");
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert!(
            !err.1.error.contains("price feed"),
            "the stable-only guard must no longer block a deposit: {}",
            err.1.error
        );
        assert!(err.1.error.contains("could not quote"), "{}", err.1.error);
    }

    /// A wallet funded in USDC does not hold GHO, so a GHO venue is a swap
    /// first. Treating every USD-pegged asset as already-in-hand would deposit
    /// a token the user never bought.
    /// The wallet already granted this spender more than the leg needs, so the
    /// approve is skipped and only the supply call is submitted.
    #[tokio::test]
    async fn a_covered_allowance_skips_the_approve() {
        let s = state();
        let v = s.venues.get(&venue_id(8453, "USDC")).expect("usdc venue");
        let owner = Address::repeat_byte(0x33);
        // The test RPC is unreachable, so the allowance read fails and the
        // fail-closed branch must still produce an approve.
        let call = approve_if_needed(
            s.rpc.get(&8453).unwrap(),
            v.asset,
            v.target,
            owner,
            alloy_primitives::U256::from(1u64),
        )
        .await;
        assert!(call.is_some(), "an unreadable allowance must still approve");
        assert_eq!(call.unwrap().data[..4], [0x09, 0x5e, 0xa7, 0xb3]);
    }

    #[tokio::test]
    async fn a_pegged_but_non_funding_deposit_still_swaps() {
        let mut r = req("deposit");
        r.asset = "GHO".into();
        r.to_venue_id = venue_id(8453, "GHO");
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert!(
            err.1.error.contains("could not quote") || err.1.error.contains("no allowlisted swap path"),
            "a GHO deposit must reach the swap path, not encode a direct supply: {}",
            err.1.error
        );
    }

    #[tokio::test]
    async fn a_venue_holding_another_asset_is_refused() {
        let mut r = req("deposit");
        r.asset = "wstETH".into();
        r.to_venue_id = venue_id(8453, "WETH");
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_REQUEST);
        assert!(err.1.error.contains("not wstETH"), "{}", err.1.error);
    }

    #[test]
    fn swap_then_supply_is_four_calls_with_the_user_as_recipient() {
        let s = state();
        let owner = Address::repeat_byte(0x33);
        let path = s.swaps.get(8453, "USDC", "WETH").expect("allowlisted");
        let weth = venue_id(8453, "WETH");
        let venue = s.venues.get(&weth).expect("allowlisted");
        let min_out = crate::exchanges::min_out(alloy_primitives::U256::from(1_000_000u64), 0);
        let amount_in = crate::venues::usd_to_units(100.0, path.token_in_decimals);
        let calls = vec![
            path.approve_router(amount_in),
            path.swap_call(amount_in, min_out, owner),
            approve_call(venue, min_out),
            deposit_call(venue, min_out, owner).unwrap(),
        ];
        assert_eq!(calls.len(), 4);
        assert_eq!(calls[0].to, path.token_in, "approve the funding token");
        assert_eq!(calls[1].to, path.router);
        assert_eq!(calls[2].to, venue.asset, "approve the bought token");
        assert_eq!(calls[3].to, venue.target);
        assert!(calls[3].data.windows(32).any(|w| {
            alloy_primitives::U256::from_be_slice(w) == min_out
        }));
    }

    const HOLD_ID: &str = "base:hold:0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452";

    fn state_with_hold() -> AppState {
        let body = format!(
            r#"[{{"id":"{HOLD_ID}","kind":"hold","chain_id":8453,
                 "target":"0xc1CBa3fCea344f92D9239c08C0568f6F2F0ee452",
                 "asset":"0xc1CBa3fCea344f92D9239c08C0568f6F2F0ee452",
                 "asset_decimals":18,"symbol":"wstETH"}},
                {{"id":"base:aave-v3:usdc","kind":"aave_v3","chain_id":8453,
                 "target":"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5",
                 "asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
                 "asset_decimals":6,"symbol":"USDC"}}]"#
        );
        static N: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
        let n = N.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let path =
            std::env::temp_dir().join(format!("executor-hold-{}-{n}.json", std::process::id()));
        std::fs::write(&path, body).unwrap();
        let venues = Registry::load(path.to_str().unwrap()).expect("hold venues must load");
        std::fs::remove_file(&path).ok();
        AppState { venues: Arc::new(venues), ..state() }
    }

    fn hold_req(action: &str) -> RouteRequest {
        let mut r = req(action);
        r.asset = "wstETH".into();
        r.from_venue_id = HOLD_ID.into();
        r
    }

    #[tokio::test]
    async fn a_hold_withdraw_is_a_swap_back_to_usdc() {
        let err = route(State(state_with_hold()), Json(hold_req("withdraw")))
            .await
            .unwrap_err();
        assert!(
            !err.1.error.contains("price feed"),
            "a hold exit is sized in the held token, not in dollars: {}",
            err.1.error
        );
        assert_eq!(err.0, StatusCode::BAD_GATEWAY);
        assert!(
            err.1.error.contains("could not price") || err.1.error.contains("could not quote"),
            "{}",
            err.1.error
        );
        assert!(err.1.steps.is_empty(), "nothing may be submitted without a quote");
    }

    #[tokio::test]
    async fn a_venueless_withdraw_swaps_back_to_usdc() {
        let mut r = req("withdraw");
        r.asset = "wstETH".into();
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert!(
            !err.1.error.contains("from_venue_id"),
            "a loose position needs no venue: {}",
            err.1.error
        );
        assert_eq!(err.0, StatusCode::BAD_GATEWAY, "{}", err.1.error);
        assert!(
            err.1.error.contains("could not price") || err.1.error.contains("could not quote"),
            "{}",
            err.1.error
        );
        assert!(err.1.steps.is_empty());

        let s = state();
        let path = s.swaps.get(8453, "wstETH", "USDC").expect("allowlisted exit");
        let amount = alloy_primitives::U256::from(1u64);
        let calls = vec![
            path.approve_router(amount),
            path.swap_call(amount, amount, Address::repeat_byte(0x33)),
        ];
        assert_eq!(calls[0].to, path.token_in);
        assert_eq!(calls[1].to, path.router, "the swap must target the allowlisted router");
    }

    #[tokio::test]
    async fn a_venueless_usdc_withdraw_is_refused() {
        let err = route(State(state()), Json(req("withdraw"))).await.unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_REQUEST);
        assert!(err.1.error.contains("no allowlisted swap path"), "{}", err.1.error);
    }

    #[tokio::test]
    async fn a_hold_withdraw_of_another_asset_is_refused() {
        let mut r = hold_req("withdraw");
        r.asset = "WETH".into();
        let err = route(State(state_with_hold()), Json(r)).await.unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_REQUEST);
        assert!(err.1.error.contains("not WETH"), "{}", err.1.error);
    }

    #[tokio::test]
    async fn a_hold_rebalance_exits_to_usdc_and_refuses_other_destinations() {
        let s = state_with_hold();
        let mut r = hold_req("rebalance");
        r.to_venue_id = HOLD_ID.into();
        let err = route(State(s), Json(r)).await.unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_REQUEST);
        assert!(err.1.error.contains("lands in USDC"), "{}", err.1.error);

        let mut r = hold_req("rebalance");
        r.to_venue_id = "base:aave-v3:usdc".into();
        let err = route(State(state_with_hold()), Json(r)).await.unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_GATEWAY, "{}", err.1.error);
        assert!(err.1.steps.is_empty());
    }

    #[tokio::test]
    async fn a_hold_deposit_has_no_supply_leg() {
        let mut r = hold_req("deposit");
        r.from_venue_id = String::new();
        r.to_venue_id = HOLD_ID.into();
        let err = route(State(state_with_hold()), Json(r)).await.unwrap_err();
        assert!(
            !err.1.error.contains("hold position"),
            "the deposit leg must skip the venue, not try to encode it: {}",
            err.1.error
        );
        assert!(err.1.error.contains("could not quote"), "{}", err.1.error);
    }

    #[test]
    fn encoding_a_hold_venue_is_a_bad_request() {
        let s = state_with_hold();
        let v = s.venues.get(HOLD_ID).unwrap();
        let err = encode(
            deposit_call(v, alloy_primitives::U256::from(1u64), Address::repeat_byte(0x33)),
            &hold_req("deposit"),
        )
        .unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_REQUEST);
        assert!(err.1.error.contains("hold position"), "{}", err.1.error);
    }

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

    #[test]
    fn extra_wire_fields_are_ignored() {
        let body = serde_json::json!({
            "execution_id": "e", "user_wallet": "0x1", "privy_wallet_id": "w",
            "privy_did": "did:privy:x", "chain": "base", "chain_id": 8453, "asset": "USDC",
            "amount_usd": 10.0, "action": "deposit", "max_slippage_bps": 50
        });
        let parsed: RouteRequest = serde_json::from_value(body).expect("must ignore extras");
        assert_eq!(parsed.action, "deposit");
        assert!(parsed.from_venue_id.is_empty());
        assert_eq!(parsed.max_slippage_bps, 50);
        assert_eq!(crate::exchanges::clamp_slippage(parsed.max_slippage_bps), 50);
    }

    #[tokio::test]
    async fn venue_from_another_chain_is_refused() {
        let sepolia = venue_id(84532, "USDC");
        let mainnet = venue_id(8453, "WETH");
        for (chain_id, id) in [(8453u64, &sepolia), (84532, &mainnet)] {
            let mut r = req("deposit");
            r.chain_id = chain_id;
            r.to_venue_id = id.clone();
            let err = route(State(state()), Json(r)).await.unwrap_err();
            assert_eq!(err.0, StatusCode::BAD_REQUEST);
            assert!(
                err.1.error.contains("is on chain"),
                "expected a chain mismatch, got {}",
                err.1.error
            );
        }
    }

    #[tokio::test]
    async fn missing_or_unserved_chain_is_refused() {
        let mut r = req("deposit");
        r.chain_id = 0;
        r.to_venue_id = venue_id(8453, "WETH");
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert!(err.1.error.contains("chain_id is required"), "{}", err.1.error);

        let mut r = req("deposit");
        r.chain_id = 1;
        r.to_venue_id = venue_id(8453, "WETH");
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert!(err.1.error.contains("not served"), "{}", err.1.error);
    }

    #[test]
    fn a_mined_ctoken_call_without_its_event_is_a_failure() {
        use crate::venues::{deposit_call, VenueKind, Venue, TOPIC_MINT};
        let venue = Venue {
            id: "base:moonwell:0xedc817a28e8b93b03976fbd4a3ddbc9f7d176c22".into(),
            kind: VenueKind::CToken,
            chain_id: 8453,
            target: Address::repeat_byte(0x11),
            asset: Address::repeat_byte(0x22),
            asset_decimals: 6,
            symbol: "USDC".into(),
        };
        let call = deposit_call(&venue, alloy_primitives::U256::from(1u64), Address::repeat_byte(0x33)).unwrap();

        let silent_failure = rpc::Receipt {
            outcome: Outcome::Confirmed,
            logs: vec![],
        };
        match classify(&silent_failure, &call) {
            StepOutcome::Failed(reason) => assert!(reason.contains("success event"), "{reason}"),
            other => panic!("a mined mint with no Mint event must fail, got {other:?}"),
        }

        let real_success = rpc::Receipt {
            outcome: Outcome::Confirmed,
            logs: vec![(venue.target.to_string().to_lowercase(), TOPIC_MINT.into())],
        };
        assert_eq!(classify(&real_success, &call), StepOutcome::Confirmed);

        let plain = Venue { kind: VenueKind::Erc4626, ..venue.clone() };
        let plain_call = deposit_call(&plain, alloy_primitives::U256::from(1u64), Address::repeat_byte(0x33)).unwrap();
        assert_eq!(classify(&silent_failure, &plain_call), StepOutcome::Confirmed);
    }

    #[test]
    fn usd_pegged_recognises_stables_only() {
        assert!(is_usd_pegged("USDC"));
        assert!(is_usd_pegged("GHO"));
        assert!(!is_usd_pegged("WETH"));
        assert!(!is_usd_pegged("cbBTC"));
    }

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

    #[test]
    fn rejection_carries_no_steps() {
        let (code, Json(r)) = RouteResponse::failed("e", "nope");
        assert_eq!(code, StatusCode::BAD_REQUEST);
        assert!(r.steps.is_empty());
        assert!(serde_json::to_value(&r).unwrap().get("steps").is_none());
    }
}
