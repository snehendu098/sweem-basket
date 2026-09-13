use alloy_primitives::{Address, Bytes, U256};
use alloy_sol_types::{sol, SolCall};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

mod aave_v3;
mod compound_v3;
mod ctoken;
mod erc4626;
mod hold;

sol! {
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

/// Skips the approve when the spender can already move this much. An
/// unreadable allowance approves anyway: a redundant approval costs gas, a
/// missing one fails the step that follows it.
pub async fn approve_if_needed(
    rpc: &crate::rpc::Rpc,
    token: Address,
    spender: Address,
    owner: Address,
    amount: U256,
) -> Option<Call> {
    let mut data = Vec::with_capacity(68);
    data.extend_from_slice(&[0xdd, 0x62, 0xed, 0x3e]); // allowance(address,address)
    data.extend_from_slice(&[0u8; 12]);
    data.extend_from_slice(owner.as_slice());
    data.extend_from_slice(&[0u8; 12]);
    data.extend_from_slice(spender.as_slice());

    match rpc.eth_call(&token.to_string(), &data).await {
        Some(out) if out.len() >= 32 && U256::from_be_slice(&out[..32]) >= amount => None,
        _ => Some(approve(token, spender, amount)),
    }
}

/// What the venue says this owner can actually take out, in asset units.
/// `None` means the protocol does not expose it cheaply and the caller should
/// send what was asked: Aave reverts on an overdraw, which is safe, while a
/// Compound fork returns an error code inside a successful transaction.
pub async fn max_withdrawable(
    rpc: &crate::rpc::Rpc,
    venue: &Venue,
    owner: Address,
) -> Option<U256> {
    // balanceOfUnderlying(address) / maxWithdraw(address) / balanceOf(address)
    let (target, selector) = match venue.kind {
        VenueKind::CToken => (venue.target, [0x3a, 0xf9, 0xe6, 0x69]),
        VenueKind::Erc4626 => (venue.target, [0xce, 0x96, 0xcb, 0x77]),
        VenueKind::CompoundV3 => (venue.target, [0x70, 0xa0, 0x82, 0x31]),
        VenueKind::AaveV3 | VenueKind::Hold => return None,
    };

    let mut data = Vec::with_capacity(36);
    data.extend_from_slice(&selector);
    data.extend_from_slice(&[0u8; 12]);
    data.extend_from_slice(owner.as_slice());

    let out = rpc.eth_call(&target.to_string(), &data).await?;
    (out.len() >= 32).then(|| U256::from_be_slice(&out[..32]))
}

pub fn approve(token: Address, spender: Address, amount: U256) -> Call {
    Call {
        to: token,
        data: approveCall { spender, amount }.abi_encode().into(),
        expect: None,
    }
}

pub fn deposit_call(venue: &Venue, amount: U256, owner: Address) -> Result<Call, String> {
    match venue.kind {
        VenueKind::Erc4626 => erc4626::deposit(venue, amount, owner),
        VenueKind::AaveV3 => aave_v3::deposit(venue, amount, owner),
        VenueKind::CompoundV3 => compound_v3::deposit(venue, amount, owner),
        VenueKind::CToken => ctoken::deposit(venue, amount, owner),
        VenueKind::Hold => hold::deposit(venue, amount, owner),
    }
}

pub fn withdraw_call(venue: &Venue, amount: U256, owner: Address) -> Result<Call, String> {
    match venue.kind {
        VenueKind::Erc4626 => erc4626::withdraw(venue, amount, owner),
        VenueKind::AaveV3 => aave_v3::withdraw(venue, amount, owner),
        VenueKind::CompoundV3 => compound_v3::withdraw(venue, amount, owner),
        VenueKind::CToken => ctoken::withdraw(venue, amount, owner),
        VenueKind::Hold => hold::withdraw(venue, amount, owner),
    }
}

pub fn usd_to_units(amount_usd: f64, decimals: u8) -> U256 {
    let scaled = amount_usd * 10f64.powi(decimals as i32);
    U256::from(scaled.max(0.0) as u128)
}

#[cfg(test)]
pub(crate) fn test_venue(kind: VenueKind) -> Venue {
    Venue {
        id: "base:morpho-blue:steakUSDC".into(),
        kind,
        chain_id: 8453,
        target: Address::repeat_byte(0x11),
        asset: Address::repeat_byte(0x22),
        asset_decimals: 6,
        symbol: "USDC".into(),
    }
}

#[cfg(test)]
mod withdrawable_tests {
    use super::*;

    /// The selectors are the contract: balanceOfUnderlying for a cToken,
    /// maxWithdraw for a vault, balanceOf for a Comet. Aave exposes none of
    /// these on the pool and reverts on an overdraw instead.
    #[test]
    fn each_kind_reads_the_right_balance() {
        use alloy_primitives::hex;
        assert_eq!(&hex::decode("3af9e669").unwrap()[..], [0x3a, 0xf9, 0xe6, 0x69]);
        assert_eq!(&hex::decode("ce96cb77").unwrap()[..], [0xce, 0x96, 0xcb, 0x77]);
        assert_eq!(&hex::decode("70a08231").unwrap()[..], [0x70, 0xa0, 0x82, 0x31]);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn vault() -> Venue {
        test_venue(VenueKind::Erc4626)
    }

    fn aave() -> Venue {
        test_venue(VenueKind::AaveV3)
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
        assert_eq!(c.data[..4], [0x09, 0x5e, 0xa7, 0xb3]);
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
