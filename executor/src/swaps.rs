//! Uniswap v3 swap legs.
//!
//! A user deposits USDC; if the asset they chose is not USDC, the protocol buys
//! it on Uniswap v3 and then supplies it to a lending venue if one exists, or
//! simply leaves it in the user's wallet.
//!
//! Two properties do the safety work here:
//!
//! 1. **Paths come from `swaps.json`, never from the request.** Same boundary as
//!    the venue allowlist: an unlisted pair cannot be swapped, whatever the
//!    wallet service asks for. A caller-supplied path is a caller-supplied
//!    destination for the funds.
//! 2. **`amountOutMinimum` is always derived from a live quote.** A swap with a
//!    minimum of 0 is an unbounded loss, so a quote that cannot be obtained
//!    fails the leg instead of defaulting.
//!
//! Routing is static, not a router algorithm, because the direct USDC pools for
//! the staking tokens are dead: measured on chain, USDC->wstETH direct costs
//! -78.4% at $1,000, while USDC-500-WETH-100-wstETH costs -0.0033%.

use alloy_primitives::{Address, Bytes, U256};
use alloy_sol_types::{sol, SolCall};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

use crate::{
    rpc::Rpc,
    venues::{approve, chain_label, Call},
};

sol! {
    // SwapRouter02 (0x2626664c2603336E57B271c5C0b26F421741e481 on Base).
    // No Permit2, no deadline, no multicall wrapper: a plain approve -> call
    // works, confirmed by simulating from a cold EOA with state overrides.
    struct ExactInputSingleParams {
        address tokenIn;
        address tokenOut;
        uint24 fee;
        address recipient;
        uint256 amountIn;
        uint256 amountOutMinimum;
        uint160 sqrtPriceLimitX96;
    }
    function exactInputSingle(ExactInputSingleParams params) external payable returns (uint256 amountOut);

    // `path` is packed token(20) | fee(3) | token(20) | ... with no padding.
    // Unlike the single-hop struct this one contains `bytes`, so it is dynamic
    // and its calldata carries an extra leading offset word. That is exactly the
    // kind of thing worth not hand-rolling; `sol!` gets it right.
    struct ExactInputParams {
        bytes path;
        address recipient;
        uint256 amountIn;
        uint256 amountOutMinimum;
    }
    function exactInput(ExactInputParams params) external payable returns (uint256 amountOut);

    // QuoterV2 (0x3d4e44Eb1374240CE5F1B871ab261CD16335B76a). Note the field
    // order differs from the router's: amountIn comes BEFORE fee here.
    struct QuoteExactInputSingleParams {
        address tokenIn;
        address tokenOut;
        uint256 amountIn;
        uint24 fee;
        uint160 sqrtPriceLimitX96;
    }
    function quoteExactInputSingle(QuoteExactInputSingleParams params)
        external
        returns (uint256 amountOut, uint160 sqrtPriceX96After, uint32 initializedTicksCrossed, uint256 gasEstimate);
    function quoteExactInput(bytes path, uint256 amountIn)
        external
        returns (uint256 amountOut, uint160[] sqrtPriceX96AfterList, uint32[] initializedTicksCrossedList, uint256 gasEstimate);
}

/// Default slippage bound. Measured impact at our sizes is under 1bp, so this is
/// a staleness buffer between quoting and inclusion rather than a price bound.
pub const DEFAULT_SLIPPAGE_BPS: u32 = 50;
/// Server-side clamp. The request carries `max_slippage_bps`; a caller must not
/// be able to widen its own loss bound arbitrarily, nor tighten it into a swap
/// that can never execute.
pub const MIN_SLIPPAGE_BPS: u32 = 10;
pub const MAX_SLIPPAGE_BPS: u32 = 300;

/// One pool in a path: the token that comes *out* of it, and the pool's fee tier
/// in hundredths of a bip (500 = 0.05%).
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct Hop {
    pub token: Address,
    pub fee: u32,
}

/// An allowlisted swap route. `hops` is ordered; the last hop's token is the
/// asset the user ends up holding.
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct SwapPath {
    pub chain_id: u64,
    /// Symbols, matching the venue symbols the wallet service routes by.
    pub from: String,
    pub to: String,
    /// SwapRouter02. Also the approval spender: it pulls with a plain
    /// `transferFrom`, which is why the existing approve pattern is enough.
    pub router: Address,
    /// QuoterV2. Non-`view` (it reverts internally and catches), but an
    /// `eth_call` against it works fine and needs no API key.
    pub quoter: Address,
    pub token_in: Address,
    pub token_in_decimals: u8,
    pub token_out_decimals: u8,
    pub hops: Vec<Hop>,
}

impl SwapPath {
    pub fn key(chain_id: u64, from: &str, to: &str) -> String {
        format!("{chain_id}:{from}->{to}")
    }

    pub fn id(&self) -> String {
        Self::key(self.chain_id, &self.from, &self.to)
    }

    /// The asset the user ends up holding.
    pub fn token_out(&self) -> Address {
        self.hops.last().expect("validated non-empty at load").token
    }

    fn single_hop(&self) -> bool {
        self.hops.len() == 1
    }

    /// Packed multi-hop encoding: token(20) | fee(3) | token(20) | fee(3) | ...
    /// Fees are big-endian uint24, so the low three bytes of the u32.
    pub fn packed(&self) -> Bytes {
        let mut out = Vec::with_capacity(20 + self.hops.len() * 23);
        out.extend_from_slice(self.token_in.as_slice());
        for hop in &self.hops {
            out.extend_from_slice(&hop.fee.to_be_bytes()[1..4]);
            out.extend_from_slice(hop.token.as_slice());
        }
        out.into()
    }

    /// Calldata for QuoterV2. Single-hop uses the cheaper struct form.
    pub fn quote_calldata(&self, amount_in: U256) -> Bytes {
        if self.single_hop() {
            quoteExactInputSingleCall {
                params: QuoteExactInputSingleParams {
                    tokenIn: self.token_in,
                    tokenOut: self.token_out(),
                    amountIn: amount_in,
                    fee: self.hops[0].fee.try_into().unwrap_or_default(),
                    sqrtPriceLimitX96: U256::ZERO.to(),
                },
            }
            .abi_encode()
            .into()
        } else {
            quoteExactInputCall {
                path: self.packed(),
                amountIn: amount_in,
            }
            .abi_encode()
            .into()
        }
    }

    /// First return word of either quoter call is `amountOut`.
    pub fn decode_quote(&self, ret: &[u8]) -> Option<U256> {
        if self.single_hop() {
            quoteExactInputSingleCall::abi_decode_returns(ret)
                .ok()
                .map(|r| r.amountOut)
        } else {
            quoteExactInputCall::abi_decode_returns(ret)
                .ok()
                .map(|r| r.amountOut)
        }
        .filter(|a| !a.is_zero())
    }

    /// The swap itself.
    ///
    /// `recipient` is the user's own wallet: the output never lands in a
    /// contract this service controls, the same property the Comet arm keeps by
    /// using `supplyTo`.
    ///
    /// `sqrtPriceLimitX96` is 0 deliberately. Any other value silently
    /// partial-fills; `amountOutMinimum` is the correct way to bound the trade.
    pub fn swap_call(&self, amount_in: U256, min_out: U256, recipient: Address) -> Call {
        let data = if self.single_hop() {
            exactInputSingleCall {
                params: ExactInputSingleParams {
                    tokenIn: self.token_in,
                    tokenOut: self.token_out(),
                    fee: self.hops[0].fee.try_into().unwrap_or_default(),
                    recipient,
                    amountIn: amount_in,
                    amountOutMinimum: min_out,
                    sqrtPriceLimitX96: U256::ZERO.to(),
                },
            }
            .abi_encode()
        } else {
            exactInputCall {
                params: ExactInputParams {
                    path: self.packed(),
                    recipient,
                    amountIn: amount_in,
                    amountOutMinimum: min_out,
                },
            }
            .abi_encode()
        };
        Call {
            to: self.router,
            data: data.into(),
            // A v3 swap that cannot meet amountOutMinimum reverts ("Too little
            // received"), so the receipt status is evidence on its own.
            expect: None,
        }
    }

    /// Approval for the router, which is its own spender.
    pub fn approve_router(&self, amount: U256) -> Call {
        approve(self.token_in, self.router, amount)
    }
}

/// Clamp the caller's slippage bound. 0 means "unset" and takes the default;
/// anything else is pulled into range rather than rejected, because a bad bound
/// on an otherwise valid deposit should not fail the user's intent.
pub fn clamp_slippage(bps: u32) -> u32 {
    match bps {
        0 => DEFAULT_SLIPPAGE_BPS,
        b => b.clamp(MIN_SLIPPAGE_BPS, MAX_SLIPPAGE_BPS),
    }
}

/// `quoted * (10_000 - slippage_bps) / 10_000`, with the clamp applied first.
pub fn min_out(quoted: U256, slippage_bps: u32) -> U256 {
    let bps = clamp_slippage(slippage_bps);
    quoted * U256::from(10_000 - bps) / U256::from(10_000u32)
}

/// Live quote via QuoterV2 over `eth_call`. `None` means no quote — which must
/// fail the leg, never fall back to a minimum of zero.
pub async fn quote(rpc: &Rpc, path: &SwapPath, amount_in: U256) -> Option<U256> {
    let ret = rpc
        .eth_call(&path.quoter.to_string(), &path.quote_calldata(amount_in))
        .await?;
    path.decode_quote(&ret)
}

/// The swap allowlist, keyed `<chain_id>:<from>-><to>`.
#[derive(Clone, Debug, Default)]
pub struct SwapRegistry(HashMap<String, SwapPath>);

#[derive(Deserialize)]
struct SwapFile {
    paths: Vec<SwapPath>,
}

impl SwapRegistry {
    pub fn load(path: &str) -> Result<Self, String> {
        let body = std::fs::read_to_string(path).map_err(|e| format!("read swaps {path}: {e}"))?;
        let file: SwapFile =
            serde_json::from_str(&body).map_err(|e| format!("parse swaps {path}: {e}"))?;
        let mut map = HashMap::new();
        for p in file.paths {
            let id = p.id();
            // Every one of these is a misconfiguration that would otherwise show
            // up as calldata pointing somewhere unintended, so it is a startup
            // failure rather than a runtime surprise.
            if chain_label(p.chain_id).is_none() {
                return Err(format!("swap {id}: unsupported chain_id {}", p.chain_id));
            }
            if p.hops.is_empty() || p.hops.len() > 3 {
                return Err(format!("swap {id}: needs 1..=3 hops, has {}", p.hops.len()));
            }
            if p.hops.iter().any(|h| h.fee == 0 || h.fee > 0xff_ffff) {
                return Err(format!("swap {id}: fee must fit a uint24 and be non-zero"));
            }
            if p.token_out() == p.token_in {
                return Err(format!("swap {id}: ends where it started"));
            }
            if p.router == p.token_in || p.quoter == p.router {
                return Err(format!("swap {id}: router/quoter/token addresses collide"));
            }
            if map.insert(id.clone(), p).is_some() {
                return Err(format!("swap {id}: duplicate path"));
            }
        }
        if map.is_empty() {
            return Err(format!("swaps {path} is empty: nothing would be swappable"));
        }
        Ok(Self(map))
    }

    /// The chain is part of the key, so a path can never be applied to the wrong
    /// network — the same guard the venues get explicitly.
    pub fn get(&self, chain_id: u64, from: &str, to: &str) -> Option<&SwapPath> {
        self.0.get(&SwapPath::key(chain_id, from, to))
    }

    pub fn len(&self) -> usize {
        self.0.len()
    }

    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    /// Every allowlisted path, sorted so the response is stable.
    pub fn all(&self) -> Vec<&SwapPath> {
        let mut out: Vec<&SwapPath> = self.0.values().collect();
        out.sort_by_key(|p| p.id());
        out
    }

    /// The public view, optionally narrowed to one chain: which pairs can be
    /// routed, and nothing about how. Routers, quoters, hop tokens and fee
    /// tiers stay here — a caller needs to know *whether* a path exists so it
    /// stops proposing legs that cannot be funded, not how it is built.
    pub fn listing(&self, chain_id: Option<u64>) -> Vec<SwapListing> {
        self.all()
            .into_iter()
            .filter(|p| chain_id.is_none_or(|id| p.chain_id == id))
            .map(|p| SwapListing {
                chain_id: p.chain_id,
                from: p.from.clone(),
                to: p.to.clone(),
                hops: p.hops.len(),
            })
            .collect()
    }
}

/// One entry of `GET /swaps`.
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct SwapListing {
    pub chain_id: u64,
    pub from: String,
    pub to: String,
    pub hops: usize,
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy_primitives::hex;

    fn registry() -> SwapRegistry {
        SwapRegistry::load("swaps.json").expect("swaps.json must parse")
    }

    fn weth() -> SwapPath {
        registry().get(8453, "USDC", "WETH").unwrap().clone()
    }

    fn wsteth() -> SwapPath {
        registry().get(8453, "USDC", "wstETH").unwrap().clone()
    }

    /// Selectors are pinned because a reordered `sol!` block would otherwise
    /// silently change which function the calldata calls. All four were
    /// confirmed against the deployed contracts on Base.
    #[test]
    fn selectors_are_pinned() {
        assert_eq!(hex::encode(exactInputSingleCall::SELECTOR), "04e45aaf");
        assert_eq!(hex::encode(exactInputCall::SELECTOR), "b858183f");
        assert_eq!(hex::encode(quoteExactInputSingleCall::SELECTOR), "c6a5026a");
        assert_eq!(hex::encode(quoteExactInputCall::SELECTOR), "cdca1753");
    }

    /// Hand-computed: USDC | 0001f4 | WETH | 000064 | wstETH, 66 bytes, no
    /// padding anywhere.
    #[test]
    fn multi_hop_path_packs_without_padding() {
        let packed = wsteth().packed();
        assert_eq!(
            hex::encode(&packed),
            "833589fcd6edb6e08f4c7c32d4f71b54bda029130001f44200000000000000000000000000000000000006000064c1cba3fcea344f92d9239c08c0568f6f2f0ee452"
        );
        assert_eq!(packed.len(), 20 + 3 + 20 + 3 + 20);
        assert_eq!(hex::encode(weth().packed()), "833589fcd6edb6e08f4c7c32d4f71b54bda029130001f44200000000000000000000000000000000000006");
    }

    /// `ExactInputParams` is dynamic (it holds `bytes`), so its calldata carries
    /// a leading offset word that the static single-hop struct does not. This is
    /// the encoding gotcha worth a test rather than a comment.
    #[test]
    fn multi_hop_calldata_has_the_leading_offset_word() {
        let user = Address::repeat_byte(0x33);
        let multi = wsteth().swap_call(U256::from(1u64), U256::from(1u64), user);
        let word0 = &multi.data[4..36];
        assert_eq!(U256::from_be_slice(word0), U256::from(32u64), "offset word");

        // The single-hop struct encodes inline: its first word is tokenIn.
        let single = weth().swap_call(U256::from(1u64), U256::from(1u64), user);
        assert_eq!(
            Address::from_slice(&single.data[16..36]),
            weth().token_in,
            "static params encode inline, no offset word"
        );
    }

    /// The output must land in the user's own wallet, never in a contract this
    /// service controls.
    #[test]
    fn recipient_is_always_the_user() {
        let user = Address::repeat_byte(0xab);
        for path in [weth(), wsteth()] {
            let call = path.swap_call(U256::from(100u64), U256::from(1u64), user);
            let found = call.data.windows(20).any(|w| w == user.as_slice());
            assert!(found, "recipient must appear in the calldata for {}", path.id());
            assert_eq!(call.to, path.router);
        }
        // And decoding it back gives exactly that address.
        let decoded =
            exactInputSingleCall::abi_decode(&weth().swap_call(U256::from(1u64), U256::from(1u64), user).data)
                .unwrap();
        assert_eq!(decoded.params.recipient, user);
        assert_eq!(decoded.params.sqrtPriceLimitX96, U256::ZERO.to::<alloy_primitives::U160>());
    }

    #[test]
    fn slippage_is_clamped_at_both_ends() {
        assert_eq!(clamp_slippage(0), DEFAULT_SLIPPAGE_BPS, "unset takes the default");
        assert_eq!(clamp_slippage(1), MIN_SLIPPAGE_BPS, "too tight is widened");
        assert_eq!(clamp_slippage(9), MIN_SLIPPAGE_BPS);
        assert_eq!(clamp_slippage(10), 10);
        assert_eq!(clamp_slippage(300), 300);
        assert_eq!(clamp_slippage(10_000), MAX_SLIPPAGE_BPS, "a caller cannot widen its own loss bound");
        assert_eq!(clamp_slippage(u32::MAX), MAX_SLIPPAGE_BPS);
    }

    #[test]
    fn min_out_applies_the_clamped_bound() {
        let quoted = U256::from(1_000_000u64);
        assert_eq!(min_out(quoted, 0), U256::from(995_000u64)); // 50bps default
        assert_eq!(min_out(quoted, 100), U256::from(990_000u64));
        // An absurd request is clamped to 300bps, not honoured.
        assert_eq!(min_out(quoted, 9_000), U256::from(970_000u64));
    }

    /// The listing is exactly what the registry holds — and carries none of the
    /// addresses or fee tiers the paths are built from.
    #[test]
    fn listing_mirrors_the_registry_without_construction_details() {
        let r = registry();
        let all = r.listing(None);
        assert_eq!(all.len(), r.len(), "one entry per allowlisted path");
        for p in r.all() {
            let got = all
                .iter()
                .find(|l| l.chain_id == p.chain_id && l.from == p.from && l.to == p.to)
                .unwrap_or_else(|| panic!("{} missing from the listing", p.id()));
            assert_eq!(got.hops, p.hops.len());
        }
        let json = serde_json::to_string(&all).unwrap();
        for leaked in ["router", "quoter", "token", "fee", "decimals"] {
            assert!(!json.contains(leaked), "{leaked} must not be exposed: {json}");
        }

        // Chain filter, same semantics as /venues.
        assert!(r.listing(Some(8453)).iter().all(|l| l.chain_id == 8453));
        assert_eq!(r.listing(Some(8453)).len(), all.len(), "all paths are on Base today");
        assert!(r.listing(Some(84532)).is_empty(), "no paths on Base Sepolia");
    }

    #[test]
    fn unlisted_paths_are_absent() {
        let r = registry();
        assert!(r.get(8453, "USDC", "WETH").is_some());
        assert!(r.get(8453, "USDC", "DOGE").is_none(), "unlisted asset");
        assert!(r.get(84532, "USDC", "WETH").is_none(), "path is chain-scoped");
        // Every entry path has its exit: a hold position that can be entered
        // and not left is worse than no position at all.
        for sym in ["WETH", "cbBTC", "wstETH", "cbETH"] {
            let out = r.get(8453, "USDC", sym).expect("entry");
            let back = r.get(8453, sym, "USDC").expect("exit");
            // The key is directional, and so is the path: the exit starts where
            // the entry ended.
            assert_eq!(back.token_in, out.token_out(), "{sym} exit must start at the token");
            assert_eq!(back.token_out(), out.token_in, "{sym} exit must end in USDC");
            assert_eq!(back.hops.len(), out.hops.len(), "{sym} exit reverses the same hops");
            let mut fees: Vec<u32> = out.hops.iter().map(|h| h.fee).collect();
            fees.reverse();
            assert_eq!(fees, back.hops.iter().map(|h| h.fee).collect::<Vec<_>>());
        }
        assert!(r.get(8453, "WETH", "DOGE").is_none(), "direction matters");
    }

    /// A quote of zero is not a quote. Treating it as one would produce
    /// amountOutMinimum: 0 — the unbounded-loss bug.
    #[test]
    fn a_zero_or_undecodable_quote_is_no_quote() {
        let p = weth();
        assert_eq!(p.decode_quote(&[0u8; 128]), None, "zero amountOut");
        assert_eq!(p.decode_quote(&[]), None, "empty revert data");
        assert_eq!(p.decode_quote(&[0u8; 7]), None, "truncated");
        let mut ok = [0u8; 128];
        ok[31] = 7;
        assert_eq!(p.decode_quote(&ok), Some(U256::from(7u64)));
    }

    /// The exit direction, pinned the same way: byte-for-byte the payload that
    /// returned 99.836789 USDC for 0.031883086156745452 wstETH on Base mainnet,
    /// hops reversed (wstETH -100- WETH -500- USDC).
    #[test]
    fn exit_quote_calldata_matches_the_verified_payload() {
        let back = registry().get(8453, "wstETH", "USDC").unwrap().clone();
        assert_eq!(
            hex::encode(back.packed()),
            "c1cba3fcea344f92d9239c08c0568f6f2f0ee45200006442000000000000000000000000000000000000060001f4833589fcd6edb6e08f4c7c32d4f71b54bda02913"
        );
        assert_eq!(
            hex::encode(back.quote_calldata(U256::from(31_883_086_156_745_452u64))),
            "cdca175300000000000000000000000000000000000000000000000000000000000000400000000000000000000000000000000000000000000000000071457f78b74aec0000000000000000000000000000000000000000000000000000000000000042c1cba3fcea344f92d9239c08c0568f6f2f0ee45200006442000000000000000000000000000000000000060001f4833589fcd6edb6e08f4c7c32d4f71b54bda02913000000000000000000000000000000000000000000000000000000000000"
        );
    }

    #[test]
    fn quote_calldata_matches_the_verified_payload() {
        // Byte-for-byte the payload that returned 0.039566 WETH for 100 USDC on
        // Base mainnet.
        let got = hex::encode(weth().quote_calldata(U256::from(100_000_000u64)));
        assert_eq!(
            got,
            "c6a5026a000000000000000000000000833589fcd6edb6e08f4c7c32d4f71b54bda029130000000000000000000000004200000000000000000000000000000000000006\
0000000000000000000000000000000000000000000000000000000005f5e10000000000000000000000000000000000000000000000000000000000000001f40000000000000000000000000000000000000000000000000000000000000000"
        );
    }

    #[test]
    fn approval_spender_is_the_router() {
        let p = weth();
        let call = p.approve_router(U256::from(5u64));
        assert_eq!(call.to, p.token_in, "approve is sent to the token");
        assert_eq!(Address::from_slice(&call.data[16..36]), p.router);
    }

    #[test]
    fn malformed_allowlists_are_refused_at_startup() {
        let dir = std::env::temp_dir().join("executor-swaps-test");
        std::fs::create_dir_all(&dir).unwrap();
        let cases = [
            (r#"{"paths":[]}"#, "empty"),
            (
                r#"{"paths":[{"chain_id":1,"from":"USDC","to":"WETH","router":"0x2626664c2603336E57B271c5C0b26F421741e481","quoter":"0x3d4e44Eb1374240CE5F1B871ab261CD16335B76a","token_in":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","token_in_decimals":6,"token_out_decimals":18,"hops":[{"token":"0x4200000000000000000000000000000000000006","fee":500}]}]}"#,
                "unsupported chain_id",
            ),
            (
                r#"{"paths":[{"chain_id":8453,"from":"USDC","to":"WETH","router":"0x2626664c2603336E57B271c5C0b26F421741e481","quoter":"0x3d4e44Eb1374240CE5F1B871ab261CD16335B76a","token_in":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","token_in_decimals":6,"token_out_decimals":18,"hops":[]}]}"#,
                "hops",
            ),
        ];
        for (i, (body, want)) in cases.iter().enumerate() {
            let path = dir.join(format!("bad-{i}.json"));
            std::fs::write(&path, body).unwrap();
            let err = SwapRegistry::load(path.to_str().unwrap()).unwrap_err();
            assert!(err.contains(want), "case {i}: {err}");
        }
    }
}

