use alloy_primitives::{Address, Bytes, U256};
use alloy_sol_types::{sol, SolCall};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

// Selectors are pinned by the tests in this file: reordering this block
// silently swaps calldata between protocols.
sol! {
    function deposit(uint256 assets, address receiver) external returns (uint256);
    function redeem(uint256 shares, address receiver, address owner) external returns (uint256);
    function withdraw(uint256 assets, address receiver, address owner) external returns (uint256);

    function supply(address asset, uint256 amount, address onBehalfOf, uint16 referralCode) external;
    #[allow(non_snake_case)]
    function withdraw(address asset, uint256 amount, address to) external returns (uint256);

    function supplyTo(address dst, address asset, uint256 amount) external;
    function withdrawTo(address to, address asset, uint256 amount) external;

    function mint(uint256 mintAmount) external returns (uint256);
    function redeemUnderlying(uint256 redeemAmount) external returns (uint256);

    function approve(address spender, uint256 amount) external returns (bool);
}

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum VenueKind {
    Erc4626,
    AaveV3,
    CompoundV3,
    // Failures are a non-zero RETURN VALUE in a *successful* transaction, which
    // is why every ctoken call carries an `expect` event.
    #[serde(rename = "ctoken")]
    CToken,
    Hold,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ExpectedEvent {
    pub address: Address,
    pub topic0: &'static str,
}

pub const TOPIC_MINT: &str = "0x4c209b5fc8ad50758f13e2e1088ba56a560dff690a1c6fef26394f4c03821c4f";
pub const TOPIC_REDEEM: &str = "0xe5b754fb1abb7f01b499791d0b820ae3b6af3424ac1c59768edb53f4ec31a929";

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct Venue {
    pub id: String,
    pub kind: VenueKind,
    pub chain_id: u64,
    pub target: Address,
    pub asset: Address,
    pub asset_decimals: u8,
    pub symbol: String,
}

pub fn chain_label(chain_id: u64) -> Option<&'static str> {
    match chain_id {
        8453 => Some("base"),
        84532 => Some("base-sepolia"),
        _ => None,
    }
}

#[derive(Clone, Debug, Default)]
pub struct Registry(HashMap<String, Venue>);

#[derive(Deserialize)]
struct GeneratedFile {
    venues: Vec<Venue>,
}

fn parse_venues(path: &str, body: &str) -> Result<Vec<Venue>, String> {
    if body.trim_start().starts_with('[') {
        return serde_json::from_str(body).map_err(|e| format!("parse venues {path}: {e}"));
    }
    serde_json::from_str::<GeneratedFile>(body)
        .map(|f| f.venues)
        .map_err(|e| format!("parse venues {path}: {e}"))
}

impl Registry {
    pub fn load(path: &str) -> Result<Self, String> {
        let body =
            std::fs::read_to_string(path).map_err(|e| format!("read venues {path}: {e}"))?;
        let venues = parse_venues(path, &body)?;
        if venues.is_empty() {
            return Err(format!("venues {path} is empty: nothing would be routable"));
        }
        for v in &venues {
            let want = chain_label(v.chain_id)
                .ok_or_else(|| format!("venue {}: unsupported chain_id {}", v.id, v.chain_id))?;
            if !v.id.starts_with(&format!("{want}:")) {
                return Err(format!(
                    "venue {}: id must start with the chain label {want:?} for chain_id {}",
                    v.id, v.chain_id
                ));
            }
            if v.kind != VenueKind::Hold && v.target == v.asset {
                return Err(format!("venue {}: target must not be the asset itself", v.id));
            }
        }
        let map: HashMap<_, _> = venues.into_iter().map(|v| (v.id.clone(), v)).collect();
        Ok(Self(map))
    }

    pub fn get(&self, id: &str) -> Option<&Venue> {
        self.0.get(id)
    }

    pub fn all(&self) -> Vec<&Venue> {
        let mut out: Vec<&Venue> = self.0.values().collect();
        out.sort_by(|a, b| a.id.cmp(&b.id));
        out
    }

    pub fn len(&self) -> usize {
        self.0.len()
    }
}

#[derive(Debug, Clone)]
pub struct Call {
    pub to: Address,
    pub data: Bytes,
    pub expect: Option<ExpectedEvent>,
}

pub fn approve_call(venue: &Venue, amount: U256) -> Call {
    approve(venue.asset, venue.target, amount)
}

pub fn approve(token: Address, spender: Address, amount: U256) -> Call {
    Call {
        to: token,
        data: approveCall { spender, amount }.abi_encode().into(),
        expect: None,
    }
}

pub fn deposit_call(venue: &Venue, amount: U256, owner: Address) -> Result<Call, String> {
    let data = match venue.kind {
        VenueKind::Erc4626 => depositCall {
            assets: amount,
            receiver: owner,
        }
        .abi_encode(),
        VenueKind::AaveV3 => supplyCall {
            asset: venue.asset,
            amount,
            onBehalfOf: owner,
            referralCode: 0,
        }
        .abi_encode(),
        VenueKind::CompoundV3 => supplyToCall {
            dst: owner,
            asset: venue.asset,
            amount,
        }
        .abi_encode(),
        VenueKind::CToken => mintCall { mintAmount: amount }.abi_encode(),
        VenueKind::Hold => return Err(hold_has_no_call(venue, "deposit")),
    };
    Ok(Call {
        to: venue.target,
        data: data.into(),
        expect: expected_event(venue, TOPIC_MINT),
    })
}

pub fn withdraw_call(venue: &Venue, amount: U256, owner: Address) -> Result<Call, String> {
    let data = match venue.kind {
        VenueKind::Erc4626 => withdraw_0Call {
            assets: amount,
            receiver: owner,
            owner,
        }
        .abi_encode(),
        VenueKind::AaveV3 => withdraw_1Call {
            asset: venue.asset,
            amount,
            to: owner,
        }
        .abi_encode(),
        VenueKind::CompoundV3 => withdrawToCall {
            to: owner,
            asset: venue.asset,
            amount,
        }
        .abi_encode(),
        VenueKind::CToken => redeemUnderlyingCall {
            redeemAmount: amount,
        }
        .abi_encode(),
        VenueKind::Hold => return Err(hold_has_no_call(venue, "withdraw")),
    };
    Ok(Call {
        to: venue.target,
        data: data.into(),
        expect: expected_event(venue, TOPIC_REDEEM),
    })
}

fn hold_has_no_call(venue: &Venue, action: &str) -> String {
    format!(
        "venue {} is a hold position: there is no {action} call, it is entered and exited by swapping {}",
        venue.id, venue.symbol
    )
}

fn expected_event(venue: &Venue, topic0: &'static str) -> Option<ExpectedEvent> {
    match venue.kind {
        VenueKind::CToken => Some(ExpectedEvent {
            address: venue.target,
            topic0,
        }),
        _ => None,
    }
}

pub fn usd_to_units(amount_usd: f64, decimals: u8) -> U256 {
    let scaled = amount_usd * 10f64.powi(decimals as i32);
    U256::from(scaled.max(0.0) as u128)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn vault() -> Venue {
        Venue {
            id: "base:morpho-blue:steakUSDC".into(),
            kind: VenueKind::Erc4626,
            chain_id: 8453,
            target: Address::repeat_byte(0x11),
            asset: Address::repeat_byte(0x22),
            asset_decimals: 6,
            symbol: "USDC".into(),
        }
    }

    fn aave() -> Venue {
        Venue {
            kind: VenueKind::AaveV3,
            ..vault()
        }
    }

    fn comet() -> Venue {
        Venue {
            kind: VenueKind::CompoundV3,
            ..vault()
        }
    }

    fn mtoken() -> Venue {
        Venue {
            kind: VenueKind::CToken,
            ..vault()
        }
    }

    #[test]
    fn ctoken_selectors_are_canonical() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        assert_eq!(
            deposit_call(&mtoken(), amt, owner).unwrap().data[..4],
            [0xa0, 0x71, 0x2d, 0x68]
        );
        assert_eq!(
            withdraw_call(&mtoken(), amt, owner).unwrap().data[..4],
            [0x85, 0x2a, 0x12, 0xe3]
        );
    }

    #[test]
    fn ctoken_calldata_is_amount_only_and_unique() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        let d = deposit_call(&mtoken(), amt, owner).unwrap();
        assert_eq!(d.data.len(), 36, "selector + one word");
        assert!(
            !d.data.windows(20).any(|w| w == owner.as_slice()),
            "mint credits msg.sender; an owner in the calldata would mean the wrong encoding"
        );
        assert_eq!(d.to, mtoken().target);
        for other in [vault(), aave(), comet()] {
            assert_ne!(d.data[..4], deposit_call(&other, amt, owner).unwrap().data[..4]);
        }
    }

    #[test]
    fn ctoken_calls_demand_their_success_event() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        let d = deposit_call(&mtoken(), amt, owner).unwrap().expect.expect("mint must be verified");
        assert_eq!(d.address, mtoken().target);
        assert_eq!(d.topic0, TOPIC_MINT);
        let w = withdraw_call(&mtoken(), amt, owner).unwrap().expect.expect("redeem must be verified");
        assert_eq!(w.topic0, TOPIC_REDEEM);

        for v in [vault(), aave(), comet()] {
            assert!(deposit_call(&v, amt, owner).unwrap().expect.is_none());
            assert!(withdraw_call(&v, amt, owner).unwrap().expect.is_none());
        }
        assert!(approve_call(&mtoken(), amt).expect.is_none(), "approve reverts on failure");
    }

    #[test]
    fn usd_conversion_respects_decimals() {
        assert_eq!(usd_to_units(1.0, 6), U256::from(1_000_000u64));
        assert_eq!(usd_to_units(0.5, 6), U256::from(500_000u64));
        assert_eq!(usd_to_units(1.0, 18), U256::from(1_000_000_000_000_000_000u64));
        assert_eq!(usd_to_units(-5.0, 6), U256::ZERO, "negative clamps to zero");
    }

    #[test]
    fn erc4626_and_aave_produce_different_selectors() {
        let owner = Address::repeat_byte(0x33);
        let amt = usd_to_units(100.0, 6);
        let v = deposit_call(&vault(), amt, owner).unwrap();
        let a = deposit_call(&aave(), amt, owner).unwrap();
        assert_ne!(v.data[..4], a.data[..4]);
        assert_eq!(v.to, vault().target);
        assert_eq!(a.to, aave().target);
    }

    #[test]
    fn approve_targets_the_asset_not_the_vault() {
        let c = approve_call(&vault(), U256::from(1u64));
        assert_eq!(c.to, vault().asset);
        assert_ne!(c.to, vault().target);
    }

    #[test]
    fn all_lists_every_venue_sorted_and_serializable() {
        let r = Registry::load("venues.json").expect("venues.json must parse");
        let all = r.all();
        assert_eq!(all.len(), r.len());
        let ids: Vec<_> = all.iter().map(|v| v.id.clone()).collect();
        let mut sorted = ids.clone();
        sorted.sort();
        assert_eq!(ids, sorted, "order must be stable");

        let json = serde_json::to_value(&all).unwrap();
        assert_eq!(json[0]["id"], all[0].id);
        assert!(json[0]["chain_id"].is_number());
        assert!(json[0]["kind"].is_string(), "kind must round-trip as its wire name");
    }

    #[test]
    fn registry_rejects_unknown_venue() {
        let r = Registry::default();
        assert!(r.get("base:evil:0xdead").is_none());
    }

    #[test]
    fn withdraw_overloads_map_to_the_right_signatures() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        assert_eq!(
            withdraw_call(&vault(), amt, owner).unwrap().data[..4],
            [0xb4, 0x60, 0xaf, 0x94]
        );
        assert_eq!(
            withdraw_call(&aave(), amt, owner).unwrap().data[..4],
            [0x69, 0x32, 0x8d, 0xec]
        );
    }

    #[test]
    fn deposit_selectors_are_canonical() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        assert_eq!(
            deposit_call(&vault(), amt, owner).unwrap().data[..4],
            [0x6e, 0x55, 0x3f, 0x65]
        );
        assert_eq!(
            deposit_call(&aave(), amt, owner).unwrap().data[..4],
            [0x61, 0x7b, 0xa0, 0x37]
        );
        assert_eq!(
            approve_call(&vault(), amt).data[..4],
            [0x09, 0x5e, 0xa7, 0xb3]
        );
    }

    #[test]
    fn withdraw_pays_out_to_the_owner() {
        let owner = Address::repeat_byte(0x33);
        for v in [vault(), aave()] {
            let data = withdraw_call(&v, U256::from(1u64), owner).unwrap().data;
            assert!(
                data.windows(20).any(|w| w == owner.as_slice()),
                "owner address must appear as the receiver"
            );
        }
    }

    fn temp_venues(name: &str, body: &str) -> String {
        let p = std::env::temp_dir().join(format!("executor-{name}-{}.json", std::process::id()));
        std::fs::write(&p, body).unwrap();
        p.to_string_lossy().into_owned()
    }

    #[test]
    fn registry_loads_a_valid_file_and_rejects_ids_not_in_it() {
        let path = temp_venues(
            "valid",
            r#"[{"id":"base:aave-v3:x","kind":"aave_v3","chain_id":8453,
                 "target":"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5",
                 "asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
                 "asset_decimals":6,"symbol":"USDC"}]"#,
        );
        let r = Registry::load(&path).expect("valid file must load");
        assert_eq!(r.len(), 1);
        assert_eq!(r.get("base:aave-v3:x").unwrap().kind, VenueKind::AaveV3);
        assert!(r.get("base:aave-v3:y").is_none());
        std::fs::remove_file(path).ok();
    }

    #[test]
    fn registry_rejects_malformed_and_missing_files() {
        let bad = temp_venues("malformed", "{ not json");
        assert!(Registry::load(&bad).unwrap_err().contains("parse venues"));
        std::fs::remove_file(bad).ok();

        let empty = temp_venues("empty", "[]");
        assert!(
            Registry::load(&empty).unwrap_err().contains("is empty"),
            "an empty allowlist must refuse to start, not silently refuse every request"
        );
        std::fs::remove_file(empty).ok();

        let unknown_kind = temp_venues(
            "kind",
            r#"[{"id":"base:a","kind":"not_a_protocol","chain_id":8453,
                 "target":"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5",
                 "asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
                 "asset_decimals":6,"symbol":"USDC"}]"#,
        );
        assert!(Registry::load(&unknown_kind).is_err(), "unknown kind must not load");
        std::fs::remove_file(unknown_kind).ok();

        assert!(Registry::load("/nonexistent/venues.json")
            .unwrap_err()
            .contains("read venues"));
    }

    #[test]
    fn shipped_allowlist_is_valid() {
        let r = Registry::load("venues.json").expect("venues.json must parse");
        let mut chains = std::collections::HashSet::new();
        let mut decimals_by_symbol: HashMap<&str, u8> = HashMap::new();
        for (id, v) in r.0.iter() {
            chains.insert(v.chain_id);
            let label = chain_label(v.chain_id).expect("supported chain");
            assert!(id.starts_with(&format!("{label}:")), "{id} mislabelled");
            if v.kind != VenueKind::Hold {
                assert_ne!(v.target, v.asset, "venue must never be the asset itself");
            }
            assert!(!v.symbol.is_empty(), "{id} has no symbol");
            assert!(
                v.asset_decimals <= 18,
                "{id} has implausible decimals {}",
                v.asset_decimals
            );
            if let Some(prev) = decimals_by_symbol.insert(&v.symbol, v.asset_decimals) {
                assert_eq!(prev, v.asset_decimals, "{} has conflicting decimals", v.symbol);
            }
        }
        assert!(chains.contains(&8453), "Base mainnet venues missing");
        assert!(chains.contains(&84532), "Base Sepolia venues missing");
    }

    #[test]
    fn both_file_shapes_load() {
        let generated = temp_venues(
            "generated",
            r#"{"_generated":{"by":"gen-venues","at":"now"},
                "venues":[{"id":"base:aave-v3:x","kind":"aave_v3","chain_id":8453,
                 "target":"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5",
                 "asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
                 "asset_decimals":6,"symbol":"USDC"}]}"#,
        );
        let r = Registry::load(&generated).expect("generated shape must load");
        assert_eq!(r.len(), 1);
        std::fs::remove_file(generated).ok();
    }

    #[test]
    fn registry_rejects_a_chain_label_that_disagrees_with_chain_id() {
        let path = temp_venues(
            "mislabelled",
            r#"[{"id":"base:aave-v3:x","kind":"aave_v3","chain_id":84532,
                 "target":"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5",
                 "asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
                 "asset_decimals":6,"symbol":"USDC"}]"#,
        );
        let err = Registry::load(&path).unwrap_err();
        assert!(err.contains("chain label"), "{err}");
        std::fs::remove_file(path).ok();

        let unknown_chain = temp_venues(
            "unknownchain",
            r#"[{"id":"base:aave-v3:x","kind":"aave_v3","chain_id":1,
                 "target":"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5",
                 "asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
                 "asset_decimals":6,"symbol":"USDC"}]"#,
        );
        assert!(Registry::load(&unknown_chain)
            .unwrap_err()
            .contains("unsupported chain_id"));
        std::fs::remove_file(unknown_chain).ok();
    }

    #[test]
    fn comet_and_aave_do_not_share_selectors() {
        let owner = Address::repeat_byte(0x33);
        let amt = usd_to_units(100.0, 6);
        for (c, a) in [
            (deposit_call(&comet(), amt, owner).unwrap(), deposit_call(&aave(), amt, owner).unwrap()),
            (withdraw_call(&comet(), amt, owner).unwrap(), withdraw_call(&aave(), amt, owner).unwrap()),
        ] {
            assert_ne!(c.data[..4], a.data[..4]);
            assert_ne!(c.data, a.data);
        }
    }

    #[test]
    fn comet_selectors_are_canonical() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        assert_eq!(
            deposit_call(&comet(), amt, owner).unwrap().data[..4],
            [0x42, 0x32, 0xcd, 0x63]
        );
        assert_eq!(
            withdraw_call(&comet(), amt, owner).unwrap().data[..4],
            [0xc3, 0xb3, 0x5a, 0x7e]
        );
    }

    #[test]
    fn comet_approval_targets_the_asset_with_comet_as_spender() {
        let c = approve_call(&comet(), U256::from(7u64));
        assert_eq!(c.to, comet().asset);
        assert_ne!(c.to, comet().target);
        assert!(
            c.data.windows(20).any(|w| w == comet().target.as_slice()),
            "the venue must be the spender"
        );
    }

    #[test]
    fn comet_names_the_owner_in_both_directions() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        for data in [
            deposit_call(&comet(), amt, owner).unwrap().data,
            withdraw_call(&comet(), amt, owner).unwrap().data,
        ] {
            assert!(data.windows(20).any(|w| w == owner.as_slice()));
            assert!(data.windows(20).any(|w| w == comet().asset.as_slice()));
        }
    }

    #[test]
    fn hold_has_no_deposit_or_withdraw_call() {
        let hold = Venue {
            kind: VenueKind::Hold,
            symbol: "wstETH".into(),
            ..vault()
        };
        let owner = Address::repeat_byte(0x33);
        for r in [
            deposit_call(&hold, U256::from(1u64), owner),
            withdraw_call(&hold, U256::from(1u64), owner),
        ] {
            let err = r.expect_err("a hold venue has no protocol call");
            assert!(err.contains("hold position"), "{err}");
            assert!(err.contains("swapping"), "the error must name the way out: {err}");
        }
    }

    #[test]
    fn registry_loads_the_hold_kind() {
        let path = temp_venues(
            "hold",
            r#"[{"id":"base:hold:0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452","kind":"hold","chain_id":8453,
                 "target":"0xc1CBa3fCea344f92D9239c08C0568f6F2F0ee452",
                 "asset":"0xc1CBa3fCea344f92D9239c08C0568f6F2F0ee452",
                 "asset_decimals":18,"symbol":"WSTETH"}]"#,
        );
        let r = Registry::load(&path).expect("hold must deserialize");
        let v = r.get("base:hold:0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452").unwrap();
        assert_eq!(v.kind, VenueKind::Hold);
        assert_eq!(serde_json::to_value(v).unwrap()["kind"], "hold");
        std::fs::remove_file(path).ok();

        let wrong_chain = temp_venues(
            "holdchain",
            r#"[{"id":"base:hold:0xc1","kind":"hold","chain_id":84532,
                 "target":"0xc1CBa3fCea344f92D9239c08C0568f6F2F0ee452",
                 "asset":"0xc1CBa3fCea344f92D9239c08C0568f6F2F0ee452",
                 "asset_decimals":18,"symbol":"WSTETH"}]"#,
        );
        assert!(Registry::load(&wrong_chain).unwrap_err().contains("chain label"));
        std::fs::remove_file(wrong_chain).ok();
    }

    #[test]
    fn registry_loads_the_compound_v3_kind() {
        let path = temp_venues(
            "comet",
            r#"[{"id":"base-sepolia:compound-v3:0x57","kind":"compound_v3","chain_id":84532,
                 "target":"0x571621Ce60Cebb0c1D442B5afb38B1663C6Bf017",
                 "asset":"0x036CbD53842c5426634e7929541eC2318f3dCF7e",
                 "asset_decimals":6,"symbol":"USDC"}]"#,
        );
        let r = Registry::load(&path).expect("compound_v3 must deserialize");
        assert_eq!(
            r.get("base-sepolia:compound-v3:0x57").unwrap().kind,
            VenueKind::CompoundV3
        );
        std::fs::remove_file(path).ok();
    }
}
