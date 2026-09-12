use alloy_primitives::Address;
use axum::{extract::State, http::StatusCode, Json};
use serde::{Deserialize, Serialize};
use std::str::FromStr;
use tracing::{info, warn};

use crate::{
    rpc::{self, Outcome, POLL_INTERVAL},
    swaps::{self, SwapPath},
    venues::{approve_call, deposit_call, usd_to_units, withdraw_call, Call, Venue, VenueKind},
    AppState,
};

/// The asset a user always deposits. A non-USDC asset is bought with it.
const FUNDING_ASSET: &str = "USDC";

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
    /// Chain the caller intends to act on. Defaulted so a caller that omits it
    /// gets a clear 400 rather than an opaque 422, but it is required: every
    /// venue the request names must live on this chain.
    #[serde(default)]
    pub chain_id: u64,
    pub amount_usd: f64,
    /// deposit | withdraw | rebalance
    pub action: String,
    #[serde(default)]
    pub from_venue_id: String,
    #[serde(default)]
    pub to_venue_id: String,
    /// Slippage bound for a swap leg, in basis points. Clamped server-side to
    /// 10..=300; 0 means unset and takes the 50bps default. The caller does not
    /// get to widen its own loss bound.
    #[serde(default)]
    pub max_slippage_bps: u32,
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

impl RouteResponse {
    /// A dependency we could not reach — notably the quoter. Distinct from a
    /// rejected request: nothing about the intent was wrong.
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

    // The executor serves both Base mainnet and Base Sepolia, so "which chain"
    // has to be stated, not inferred. Inferring it from the venue is how a
    // Sepolia venue ends up executing a mainnet intent.
    if req.chain_id == 0 {
        return Err(RouteResponse::failed(&req.execution_id, "chain_id is required"));
    }
    if !state.rpc.contains_key(&req.chain_id) {
        return Err(RouteResponse::failed(
            &req.execution_id,
            format!("chain {} is not served by this executor", req.chain_id),
        ));
    }

    // Resolve every referenced venue against the allowlist BEFORE building any
    // calldata. An unknown venue is refused outright — this is the security
    // boundary that stops the wallet service from directing funds anywhere.
    let from = lookup(&state, &req.from_venue_id, &req.execution_id)?;
    let to = lookup(&state, &req.to_venue_id, &req.execution_id)?;

    // And it must be on the chain the caller asked for. Mainnet moves real
    // money: a venue from the other network reaching this point is the single
    // worst failure available, so it is checked before any calldata exists.
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
            // The user always funds in USDC. If the asset they chose is USDC and
            // a venue holds it, that is the original one-approve-one-supply
            // path. Anything else is bought on Uniswap v3 first.
            match to {
                Some(v) if v.symbol == req.asset && is_usd_pegged(&req.asset) => {
                    let amount = units(&req, v)?;
                    vec![approve_call(v, amount), encode(deposit_call(v, amount, owner), &req)?]
                }
                _ => swap_deposit(&state, &req, to, owner).await?,
            }
        }
        "withdraw" => {
            let v = from.ok_or_else(|| {
                RouteResponse::failed(&req.execution_id, "withdraw requires from_venue_id")
            })?;
            if v.kind == VenueKind::Hold {
                // Nothing to redeem: the position is the token in the wallet, so
                // leaving it is a swap back to USDC.
                hold_exit(&state, &req, v, owner).await?.0
            } else {
                let amount = units(&req, v)?;
                vec![encode(withdraw_call(v, amount, owner), &req)?]
            }
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
            if src.kind == VenueKind::Hold {
                // The exit lands in USDC, so that is what the destination has to
                // hold. Anything else would need a second swap leg sized off the
                // first one's guaranteed minimum; refuse rather than half-build it.
                if dst.symbol != FUNDING_ASSET {
                    return Err(RouteResponse::failed(
                        &req.execution_id,
                        format!(
                            "leaving hold venue {} lands in {FUNDING_ASSET}, but {} holds {}",
                            src.id, dst.id, dst.symbol
                        ),
                    ));
                }
                let (mut calls, usdc) = hold_exit(&state, &req, src, owner).await?;
                // The guaranteed minimum, not the quote: the swap may deliver
                // less, and supplying more than the wallet holds reverts.
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

        // Nothing depends on the last call, so it normally needs no
        // confirmation — unless the call is one whose failure is a return value
        // rather than a revert (Moonwell's mTokens). Those must be verified, and
        // they are usually exactly the last call in the sequence.
        if i + 1 == total && call.expect.is_none() {
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

/// Buy `req.asset` with USDC, then supply it if a venue was named.
///
/// Sizing is done on the INPUT side: the user says "$100", that is 100 USDC,
/// and the quote decides how much of the output asset that buys. Sizing in the
/// output asset would need a price feed this executor does not have.
async fn swap_deposit(
    state: &AppState,
    req: &RouteRequest,
    to: Option<&Venue>,
    owner: Address,
) -> Result<Vec<Call>, (StatusCode, Json<RouteResponse>)> {
    // A hold venue has nothing to deposit into: the swap output sitting in the
    // user's wallet already is the position. Same shape as `to: None`.
    let to = to.filter(|v| v.kind != VenueKind::Hold);
    // A venue can only receive what it actually holds.
    if let Some(v) = to {
        if v.symbol != req.asset {
            return Err(RouteResponse::failed(
                &req.execution_id,
                format!("venue {} holds {}, not {}", v.id, v.symbol, req.asset),
            ));
        }
    }

    // The allowlist, and the only source of a path. A pair absent from
    // swaps.json is refused exactly as an unlisted venue is.
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
    // Belt and braces: the key already carries the chain, but the same guard the
    // venues get is worth stating where money moves.
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

    // No quote, no swap. Submitting with amountOutMinimum: 0 would let the
    // whole input be taken for dust, so an unreachable quoter fails the leg.
    let rpc = state.rpc.get(&req.chain_id).expect("chain checked by caller");
    let quoted = swaps::quote(rpc, path, amount_in).await.ok_or_else(|| {
        RouteResponse::upstream(
            &req.execution_id,
            format!(
                "could not quote {} on chain {}: refusing to swap without a minimum output",
                path.id(),
                req.chain_id
            ),
        )
    })?;
    let min_out = swaps::min_out(quoted, req.max_slippage_bps);
    if min_out.is_zero() {
        return Err(RouteResponse::upstream(
            &req.execution_id,
            "quote is too small to bound: refusing to swap",
        ));
    }
    info!(
        execution_id = %req.execution_id, path = %path.id(),
        amount_in = %amount_in, quoted = %quoted, min_out = %min_out,
        slippage_bps = swaps::clamp_slippage(req.max_slippage_bps),
        "swap leg quoted"
    );

    let mut calls = vec![
        path.approve_router(amount_in),
        path.swap_call(amount_in, min_out, owner),
    ];
    // Supply the guaranteed-minimum, not the quote: the swap may deliver less,
    // and a supply for more than the wallet holds would revert the whole leg.
    if let Some(v) = to {
        calls.push(approve_call(v, min_out));
        calls.push(encode(deposit_call(v, min_out, owner), req)?);
    }
    Ok(calls)
}

/// Leave a hold position: sell the held token back to USDC, into the user's own
/// wallet. Returns the legs and the guaranteed-minimum USDC they produce, which
/// is what a rebalance may then deposit.
async fn hold_exit(
    state: &AppState,
    req: &RouteRequest,
    venue: &Venue,
    owner: Address,
) -> Result<(Vec<Call>, alloy_primitives::U256), (StatusCode, Json<RouteResponse>)> {
    if venue.symbol != req.asset {
        return Err(RouteResponse::failed(
            &req.execution_id,
            format!("venue {} holds {}, not {}", venue.id, venue.symbol, req.asset),
        ));
    }
    // The exit direction is its own allowlist entry. A venue that can be entered
    // and not left is worse than no venue, which is why these paths exist.
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
    let rpc = state.rpc.get(&req.chain_id).expect("chain checked by caller");
    // Same rule as the deposit leg: no quote, no swap. amountOutMinimum: 0 on
    // the way out would hand the whole position to a sandwich.
    let quoted = swaps::quote(rpc, path, amount_in).await.ok_or_else(|| {
        RouteResponse::upstream(
            &req.execution_id,
            format!(
                "could not quote {} on chain {}: refusing to swap without a minimum output",
                path.id(),
                req.chain_id
            ),
        )
    })?;
    let min_out = swaps::min_out(quoted, req.max_slippage_bps);
    if min_out.is_zero() {
        return Err(RouteResponse::upstream(
            &req.execution_id,
            "quote is too small to bound: refusing to swap",
        ));
    }
    info!(
        execution_id = %req.execution_id, path = %path.id(),
        amount_in = %amount_in, quoted = %quoted, min_out = %min_out,
        slippage_bps = swaps::clamp_slippage(req.max_slippage_bps),
        "hold exit quoted"
    );
    Ok((
        vec![
            path.approve_router(amount_in),
            path.swap_call(amount_in, min_out, owner),
        ],
        min_out,
    ))
}

/// How many units of the held token `req.amount_usd` is worth.
///
/// A hold position is denominated in the token, not in dollars — `units()`
/// refuses non-stables for exactly that reason and keeps doing so. The price
/// comes from the entry path's live quote rather than from a feed this executor
/// does not have: the same quoter, the same pools the exit will trade through.
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
    swaps::quote(state.rpc.get(&req.chain_id).expect("chain checked by caller"), entry, usd)
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

/// A venue kind with no protocol call (`hold`) refuses to encode one. That is a
/// bad request, not a panic.
fn encode(
    call: Result<Call, String>,
    req: &RouteRequest,
) -> Result<Call, (StatusCode, Json<RouteResponse>)> {
    call.map_err(|e| RouteResponse::failed(&req.execution_id, e))
}

/// What a mined transaction actually did.
#[derive(Debug, PartialEq, Eq)]
pub enum StepOutcome {
    Confirmed,
    Failed(String),
}

/// A successful receipt is not always evidence the call did anything: a
/// Compound-v2 fork (Moonwell) returns an error code inside a transaction that
/// succeeds. When a call declared a success event, its absence is a failure —
/// calling it confirmed would record a deposit of money that never moved.
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
    // usd_to_units is only correct for USD-pegged assets. Anything else needs a
    // price feed and a swap leg, which this executor does not do yet.
    if venue.symbol != req.asset {
        return Err(RouteResponse::failed(
            &req.execution_id,
            format!("venue {} holds {}, not {}", venue.id, venue.symbol, req.asset),
        ));
    }
    if !is_usd_pegged(&venue.symbol) {
        // Deposits no longer reach here for non-stables: they are sized on the
        // USDC input and the quote decides the output. Withdrawals and
        // rebalances still have nothing that can convert a USD figure into an
        // amount of WETH, so for those this remains the correct refusal.
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

    /// The allowlist is generated, so tests resolve venues by their properties
    /// rather than by a hardcoded id that regeneration can quietly remove.
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
                crate::swaps::SwapRegistry::load("swaps.json").expect("swaps.json must parse"),
            ),
            // Unreachable in these tests: every one is rejected before submission.
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

    /// Property 1: an id absent from the allowlist is refused before any
    /// calldata is built, so no network call is made and this test needs none.
    #[tokio::test]
    async fn unlisted_venue_is_refused() {
        let mut r = req("deposit");
        r.to_venue_id = "base:evil:0xdeadbeef".into();
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_REQUEST);
        assert!(err.1.error.contains("not allowlisted"), "{}", err.1.error);
        assert_eq!(err.1.status, "failed");
    }

    /// An asset with no allowlisted path is refused before any calldata or any
    /// network call, exactly as an unlisted venue is.
    #[tokio::test]
    async fn unlisted_swap_path_is_refused() {
        let mut r = req("deposit");
        r.asset = "DOGE".into();
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_REQUEST);
        assert!(err.1.error.contains("no allowlisted swap path"), "{}", err.1.error);

        // Listed on mainnet, absent on Sepolia: the chain is part of the key.
        let mut r = req("deposit");
        r.asset = "WETH".into();
        r.chain_id = 84532;
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert!(err.1.error.contains("no allowlisted swap path"), "{}", err.1.error);
    }

    /// The single worst failure available here: submitting a swap with
    /// amountOutMinimum 0. The test RPC points at a dead port, so the quote
    /// cannot be obtained — and the leg must fail rather than default.
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

    /// Depositing into a WETH venue now routes through a swap instead of being
    /// refused as "needs a price feed" — and still cannot proceed without a
    /// quote.
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

    /// A venue that does not hold the asset the user asked for is refused before
    /// the quote — a swap must not deliver wstETH to a WETH market.
    #[tokio::test]
    async fn a_venue_holding_another_asset_is_refused() {
        let mut r = req("deposit");
        r.asset = "wstETH".into();
        r.to_venue_id = venue_id(8453, "WETH");
        let err = route(State(state()), Json(r)).await.unwrap_err();
        assert_eq!(err.0, StatusCode::BAD_REQUEST);
        assert!(err.1.error.contains("not wstETH"), "{}", err.1.error);
    }

    /// The four-call shape a swap-then-supply deposit produces, built from the
    /// same pieces the handler uses. Order matters: nothing can be supplied
    /// before it has been bought.
    #[test]
    fn swap_then_supply_is_four_calls_with_the_user_as_recipient() {
        let s = state();
        let owner = Address::repeat_byte(0x33);
        let path = s.swaps.get(8453, "USDC", "WETH").expect("allowlisted");
        let weth = venue_id(8453, "WETH");
        let venue = s.venues.get(&weth).expect("allowlisted");
        let min_out = crate::swaps::min_out(alloy_primitives::U256::from(1_000_000u64), 0);
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
        // The supplied amount is the guaranteed minimum, never the quote.
        assert!(calls[3].data.windows(32).any(|w| {
            alloy_primitives::U256::from_be_slice(w) == min_out
        }));
    }

    /// venues.json is generated and carries no hold row yet, so these tests
    /// bring their own: the executor's half of the contract is that such a row
    /// loads and routes, whatever the generator emits today.
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
        // Unique per call: these tests run in parallel and would otherwise
        // delete each other's file mid-read.
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

    /// The gap this exists to close: a hold position must be exitable. The exit
    /// is a swap, so with a dead quoter it must fail the leg — never fall back
    /// to "non-stable asset needs a price feed", and never submit anything.
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

    /// Same for the rebalance leg — and the destination has to be able to
    /// receive USDC, which is what the exit produces.
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

    /// Entering a hold venue is the deposit swap with no supply leg: the token
    /// in the wallet is the position. The hold venue must not reach
    /// `deposit_call`, which has no calldata to produce for it.
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

    /// Belt and braces on the same boundary: if a hold venue ever did reach the
    /// encoder, it refuses rather than pointing calldata at a meaningless target.
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

    /// The Go wallet client sends privy_did and chain, which this handler
    /// ignores; max_slippage_bps is read and clamped. Unknown fields must not
    /// fail the request.
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
        // max_slippage_bps is no longer ignored: it reaches the swap leg, and
        // is clamped there.
        assert_eq!(parsed.max_slippage_bps, 50);
        assert_eq!(crate::swaps::clamp_slippage(parsed.max_slippage_bps), 50);
    }

    /// The guard this whole two-chain build exists to make impossible: a
    /// Sepolia venue must never be actioned on a mainnet request, or the reverse.
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

    /// Moonwell's mTokens report several failures as a RETURN VALUE inside a
    /// transaction that succeeds — verified on chain: `redeemUnderlying(1e6)`
    /// with no position returns 9, status 0x1. Believing the status would book a
    /// withdrawal that never happened, so the success event decides.
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

        // A venue whose failures revert needs no event: status is evidence.
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
