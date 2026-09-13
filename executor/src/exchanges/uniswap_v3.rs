use alloy_primitives::{Address, Bytes, U256};
use alloy_sol_types::{sol, SolCall};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

use crate::{
    rpc::Rpc,
    venues::{approve, chain_label, Call},
};

// Selectors and payloads are pinned by the tests in this file.
sol! {
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

    // Holds `bytes`, so it is dynamic: this calldata carries a leading offset
    // word that exactInputSingle's does not.
    struct ExactInputParams {
        bytes path;
        address recipient;
        uint256 amountIn;
        uint256 amountOutMinimum;
    }
    function exactInput(ExactInputParams params) external payable returns (uint256 amountOut);

    // amountIn comes BEFORE fee here, transposed against the router's struct:
    // swapping them is a valid call with absurd arguments, not a decode error.
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

pub const DEFAULT_SLIPPAGE_BPS: u32 = 50;
pub const MIN_SLIPPAGE_BPS: u32 = 10;
pub const MAX_SLIPPAGE_BPS: u32 = 300;

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct Hop {
    pub token: Address,
    pub fee: u32,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct SwapPath {
    pub chain_id: u64,
    pub from: String,
    pub to: String,
    pub router: Address,
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

    pub fn token_out(&self) -> Address {
        self.hops.last().expect("validated non-empty at load").token
    }

    fn single_hop(&self) -> bool {
        self.hops.len() == 1
    }

    pub fn packed(&self) -> Bytes {
        let mut out = Vec::with_capacity(20 + self.hops.len() * 23);
        out.extend_from_slice(self.token_in.as_slice());
        for hop in &self.hops {
            out.extend_from_slice(&hop.fee.to_be_bytes()[1..4]);
            out.extend_from_slice(hop.token.as_slice());
        }
        out.into()
    }

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
                    // Any non-zero limit silently partial-fills; amountOutMinimum
                    // is the real bound.
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
            expect: None,
        }
    }

    pub fn approve_router(&self, amount: U256) -> Call {
        approve(self.token_in, self.router, amount)
    }
}

pub fn clamp_slippage(bps: u32) -> u32 {
    match bps {
        0 => DEFAULT_SLIPPAGE_BPS,
        b => b.clamp(MIN_SLIPPAGE_BPS, MAX_SLIPPAGE_BPS),
    }
}

pub fn min_out(quoted: U256, slippage_bps: u32) -> U256 {
    let bps = clamp_slippage(slippage_bps);
    quoted * U256::from(10_000 - bps) / U256::from(10_000u32)
}

pub async fn quote(rpc: &Rpc, path: &SwapPath, amount_in: U256) -> Option<U256> {
    let ret = rpc
        .eth_call(&path.quoter.to_string(), &path.quote_calldata(amount_in))
        .await?;
    path.decode_quote(&ret)
}

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

    pub fn get(&self, chain_id: u64, from: &str, to: &str) -> Option<&SwapPath> {
        self.0.get(&SwapPath::key(chain_id, from, to))
    }

    pub fn len(&self) -> usize {
        self.0.len()
    }

    pub fn all(&self) -> Vec<&SwapPath> {
        let mut out: Vec<&SwapPath> = self.0.values().collect();
        out.sort_by_key(|p| p.id());
        out
    }

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

    #[test]
    fn selectors_are_pinned() {
        assert_eq!(hex::encode(exactInputSingleCall::SELECTOR), "04e45aaf");
        assert_eq!(hex::encode(exactInputCall::SELECTOR), "b858183f");
        assert_eq!(hex::encode(quoteExactInputSingleCall::SELECTOR), "c6a5026a");
        assert_eq!(hex::encode(quoteExactInputCall::SELECTOR), "cdca1753");
    }

    #[test]
    fn multi_hop_path_packs_without_padding() {
        let packed = wsteth().packed();
        assert_eq!(
            hex::encode(&packed),
            "833589fcd6edb6e08f4c7c32d4f71b54bda029130001f44200000000000000000000000000000000000006000064c1cba3fcea344f92d9239c08c0568f6f2f0ee452"
        );
        assert_eq!(packed.len(), 20 + 3 + 20 + 3 + 20);
        assert_eq!(hex::encode(weth().packed()), "833589fcd6edb6e08f4c7c32d4f71b54bda029130001f44200000000000000000000000000000000000006");

        let weeth = registry().get(8453, "USDC", "weETH").unwrap().packed();
        assert_eq!(
            hex::encode(&weeth),
            "833589fcd6edb6e08f4c7c32d4f71b54bda029130001f4420000000000000000000000000000000000000600006404c0599ae5a44757c0af6f9ec3b93da8976c150a"
        );
        let aero = registry().get(8453, "AERO", "USDC").unwrap().packed();
        assert_eq!(
            hex::encode(&aero),
            "940181a94a35a4569e4529a3cdfb74e38fd98631000bb842000000000000000000000000000000000000060001f4833589fcd6edb6e08f4c7c32d4f71b54bda02913"
        );

        let wbtc = registry().get(8453, "USDC", "WBTC").unwrap().packed();
        assert_eq!(
            hex::encode(&wbtc),
            "833589fcd6edb6e08f4c7c32d4f71b54bda029130001f4cbb7c0000ab88b473b1f5afd9ef808440eed33bf0000640555e30da8f98308edb960aa94c0db47230d2b9c"
        );
        let virtual_ = registry().get(8453, "VIRTUAL", "USDC").unwrap().packed();
        assert_eq!(
            hex::encode(&virtual_),
            "0b3e328455c4059eeb9e3f84b5543f74e24e7e1b0001f442000000000000000000000000000000000000060001f4833589fcd6edb6e08f4c7c32d4f71b54bda02913"
        );
    }

    #[test]
    fn multi_hop_calldata_has_the_leading_offset_word() {
        let user = Address::repeat_byte(0x33);
        let multi = wsteth().swap_call(U256::from(1u64), U256::from(1u64), user);
        let word0 = &multi.data[4..36];
        assert_eq!(U256::from_be_slice(word0), U256::from(32u64), "offset word");

        let single = weth().swap_call(U256::from(1u64), U256::from(1u64), user);
        assert_eq!(
            Address::from_slice(&single.data[16..36]),
            weth().token_in,
            "static params encode inline, no offset word"
        );
    }

    #[test]
    fn recipient_is_always_the_user() {
        let user = Address::repeat_byte(0xab);
        for path in [weth(), wsteth()] {
            let call = path.swap_call(U256::from(100u64), U256::from(1u64), user);
            let found = call.data.windows(20).any(|w| w == user.as_slice());
            assert!(found, "recipient must appear in the calldata for {}", path.id());
            assert_eq!(call.to, path.router);
        }
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
        assert_eq!(min_out(quoted, 0), U256::from(995_000u64));
        assert_eq!(min_out(quoted, 100), U256::from(990_000u64));
        assert_eq!(min_out(quoted, 9_000), U256::from(970_000u64));
    }

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

        assert!(r.listing(Some(8453)).iter().all(|l| l.chain_id == 8453));
        assert!(r.listing(Some(84532)).iter().all(|l| l.chain_id == 84532));
        assert_eq!(
            r.listing(Some(8453)).len() + r.listing(Some(84532)).len(),
            all.len(),
            "every path belongs to exactly one served chain"
        );
    }

    #[test]
    fn unlisted_paths_are_absent() {
        let r = registry();
        assert!(r.get(8453, "USDC", "WETH").is_some());
        assert!(r.get(8453, "USDC", "DOGE").is_none(), "unlisted asset");
        assert!(r.get(84532, "USDC", "AERO").is_none(), "path is chain-scoped");
        assert!(r.get(84532, "USDC", "WETH").is_some(), "the testnet pair is routable");
        for sym in ["tBTC", "LINK"] {
            assert!(r.get(8453, "USDC", sym).is_none(), "{sym}: no v3 route under the impact bound");
            assert!(r.get(8453, sym, "USDC").is_none(), "{sym}: refused in both directions");
        }
        for sym in [
            "WETH", "cbBTC", "wstETH", "cbETH", "AERO", "EURC", "GHO", "USDS", "USDbC", "rETH",
            "weETH", "DAI", "USDT", "USDe", "VIRTUAL", "WBTC", "AAVE", "MORPHO", "VVV",
        ] {
            let out = r.get(8453, "USDC", sym).expect("entry");
            let back = r.get(8453, sym, "USDC").expect("exit");
            assert_eq!(back.token_in, out.token_out(), "{sym} exit must start at the token");
            assert_eq!(back.token_out(), out.token_in, "{sym} exit must end in USDC");
            assert_eq!(back.hops.len(), out.hops.len(), "{sym} exit reverses the same hops");
            let mut fees: Vec<u32> = out.hops.iter().map(|h| h.fee).collect();
            fees.reverse();
            assert_eq!(fees, back.hops.iter().map(|h| h.fee).collect::<Vec<_>>());
        }
        assert!(r.get(8453, "WETH", "DOGE").is_none(), "direction matters");
    }

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

