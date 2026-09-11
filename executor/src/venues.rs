use alloy_primitives::{Address, Bytes, U256};
use alloy_sol_types::{sol, SolCall};
use serde::Deserialize;
use std::collections::HashMap;

sol! {
    // ERC-4626: covers Morpho vaults, Spark, Euler, Fluid, Gauntlet, yo.
    function deposit(uint256 assets, address receiver) external returns (uint256);
    function redeem(uint256 shares, address receiver, address owner) external returns (uint256);
    function withdraw(uint256 assets, address receiver, address owner) external returns (uint256);

    // Aave v3 Pool
    function supply(address asset, uint256 amount, address onBehalfOf, uint16 referralCode) external;
    #[allow(non_snake_case)]
    function withdraw(address asset, uint256 amount, address to) external returns (uint256);

    // Compound v3 (Comet). The `To` variants pin the destination explicitly.
    // Bare supply/withdraw credit msg.sender implicitly, which is the same
    // account today — but the executor already names the owner for ERC-4626 and
    // Aave, and an implicit destination is one assumption fewer worth keeping.
    function supplyTo(address dst, address asset, uint256 amount) external;
    function withdrawTo(address to, address asset, uint256 amount) external;

    // ERC-20
    function approve(address spender, uint256 amount) external returns (bool);
}

/// How a venue's deposit/withdraw calldata is shaped. Adding a protocol means
/// adding a variant here and a match arm below — nothing else changes.
#[derive(Clone, Copy, Debug, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum VenueKind {
    /// Standard tokenized vault. One adapter covers most of Base's yield.
    Erc4626,
    /// Aave v3 Pool: supply/withdraw take the asset address explicitly.
    AaveV3,
    /// Compound v3 (Comet). One contract per base asset, and it is its own
    /// spender: `supply` pulls with `transferFrom(msg.sender, comet, amount)`.
    CompoundV3,
}

#[derive(Clone, Debug, Deserialize)]
pub struct Venue {
    /// Matches the market-data service's venue ID: `chain:project:pool`.
    pub id: String,
    pub kind: VenueKind,
    pub chain_id: u64,
    /// Contract the executor calls: the vault, or the Aave Pool.
    pub target: Address,
    /// Underlying ERC-20 being supplied.
    pub asset: Address,
    pub asset_decimals: u8,
    pub symbol: String,
}

/// The allowlist. A venue absent from here cannot be transacted with, no matter
/// what the wallet service asks for.
#[derive(Clone, Debug, Default)]
pub struct Registry(HashMap<String, Venue>);

impl Registry {
    pub fn load(path: &str) -> Result<Self, String> {
        let body =
            std::fs::read_to_string(path).map_err(|e| format!("read venues {path}: {e}"))?;
        let venues: Vec<Venue> =
            serde_json::from_str(&body).map_err(|e| format!("parse venues {path}: {e}"))?;
        if venues.is_empty() {
            // An empty allowlist refuses every request. That is a misconfiguration
            // wearing the costume of a code bug, so fail loudly at startup instead.
            return Err(format!("venues {path} is empty: nothing would be routable"));
        }
        let map: HashMap<_, _> = venues.into_iter().map(|v| (v.id.clone(), v)).collect();
        Ok(Self(map))
    }

    pub fn get(&self, id: &str) -> Option<&Venue> {
        self.0.get(id)
    }

    pub fn len(&self) -> usize {
        self.0.len()
    }

    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }
}

/// One transaction to submit, already encoded.
#[derive(Debug, Clone)]
pub struct Call {
    pub to: Address,
    pub data: Bytes,
}

/// Approval the vault needs before it can pull funds. Returned separately so the
/// caller can decide whether it is already in place.
pub fn approve_call(venue: &Venue, amount: U256) -> Call {
    Call {
        to: venue.asset,
        data: approveCall {
            spender: venue.target,
            amount,
        }
        .abi_encode()
        .into(),
    }
}

/// Encode a deposit of `amount` (in the asset's smallest unit) on behalf of `owner`.
pub fn deposit_call(venue: &Venue, amount: U256, owner: Address) -> Call {
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
    };
    Call {
        to: venue.target,
        data: data.into(),
    }
}

/// Encode a withdrawal of `amount` back to `owner`.
pub fn withdraw_call(venue: &Venue, amount: U256, owner: Address) -> Call {
    let data = match venue.kind {
        // ERC-4626 withdraw is denominated in assets, which is what we track.
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
        // Comet accepts type(uint256).max as "the whole balance", verified on
        // the deployed contract. Not used: the caller asks for a USD amount, and
        // a full exit is a different intent that nothing expresses yet.
        VenueKind::CompoundV3 => withdrawToCall {
            to: owner,
            asset: venue.asset,
            amount,
        }
        .abi_encode(),
    };
    Call {
        to: venue.target,
        data: data.into(),
    }
}

/// Convert a USD amount to the asset's smallest unit.
///
/// Correct only while the asset is a USD stablecoin. Non-stable assets must be
/// priced first — see the caller, which rejects them.
pub fn usd_to_units(amount_usd: f64, decimals: u8) -> U256 {
    let scaled = amount_usd * 10f64.powi(decimals as i32);
    U256::from(scaled.max(0.0) as u128)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn vault() -> Venue {
        Venue {
            id: "Base:morpho-blue:steakUSDC".into(),
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
        let v = deposit_call(&vault(), amt, owner);
        let a = deposit_call(&aave(), amt, owner);
        assert_ne!(v.data[..4], a.data[..4]);
        // Both must target the venue contract, never the asset.
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
    fn registry_rejects_unknown_venue() {
        let r = Registry::default();
        assert!(r.get("Base:evil:0xdead").is_none());
    }

    /// Guards the alloy overload naming: `withdraw_0Call` / `withdraw_1Call` are
    /// assigned by declaration order in the sol! block, so reordering those two
    /// lines would silently swap ERC-4626 and Aave calldata. Selectors are the
    /// canonical keccak prefixes of each signature.
    #[test]
    fn withdraw_overloads_map_to_the_right_signatures() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        // withdraw(uint256,address,address)
        assert_eq!(
            withdraw_call(&vault(), amt, owner).data[..4],
            [0xb4, 0x60, 0xaf, 0x94]
        );
        // withdraw(address,uint256,address)
        assert_eq!(
            withdraw_call(&aave(), amt, owner).data[..4],
            [0x69, 0x32, 0x8d, 0xec]
        );
    }

    #[test]
    fn deposit_selectors_are_canonical() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        // deposit(uint256,address)
        assert_eq!(
            deposit_call(&vault(), amt, owner).data[..4],
            [0x6e, 0x55, 0x3f, 0x65]
        );
        // supply(address,uint256,address,uint16)
        assert_eq!(
            deposit_call(&aave(), amt, owner).data[..4],
            [0x61, 0x7b, 0xa0, 0x37]
        );
        // approve(address,uint256)
        assert_eq!(
            approve_call(&vault(), amt).data[..4],
            [0x09, 0x5e, 0xa7, 0xb3]
        );
    }

    /// Withdrawn funds must land in the user's own wallet, nowhere else. The
    /// receiver is the last 20 bytes of a 32-byte word in the calldata.
    #[test]
    fn withdraw_pays_out_to_the_owner() {
        let owner = Address::repeat_byte(0x33);
        for v in [vault(), aave()] {
            let data = withdraw_call(&v, U256::from(1u64), owner).data;
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
            r#"[{"id":"Base:aave-v3:x","kind":"aave_v3","chain_id":8453,
                 "target":"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5",
                 "asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
                 "asset_decimals":6,"symbol":"USDC"}]"#,
        );
        let r = Registry::load(&path).expect("valid file must load");
        assert_eq!(r.len(), 1);
        assert_eq!(r.get("Base:aave-v3:x").unwrap().kind, VenueKind::AaveV3);
        assert!(r.get("Base:aave-v3:y").is_none());
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
            r#"[{"id":"a","kind":"ctoken","chain_id":8453,
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

    /// The shipped allowlist must parse — a typo here is a startup failure in
    /// production, and the executor refuses to start half-configured.
    #[test]
    fn shipped_allowlist_is_valid() {
        let r = Registry::load("venues.json").expect("venues.json must parse");
        // Base Sepolia's whole lending universe is Aave's two reserves.
        assert!(r.len() >= 2);
        for (_, v) in r.0.iter() {
            assert_eq!(v.chain_id, 84532, "allowlist is Base Sepolia");
            assert_ne!(v.target, v.asset, "venue must never be the asset itself");
            match v.symbol.as_str() {
                "USDC" => assert_eq!(v.asset_decimals, 6),
                "WETH" => assert_eq!(v.asset_decimals, 18),
                other => panic!("unexpected symbol {other} in allowlist"),
            }
        }
    }

    /// Comet's supply is `(asset, amount)` while Aave's is
    /// `(asset, amount, onBehalfOf, referralCode)`. Encoding one as the other
    /// would put an amount where an address belongs, so the two must never
    /// share a selector.
    #[test]
    fn comet_and_aave_do_not_share_selectors() {
        let owner = Address::repeat_byte(0x33);
        let amt = usd_to_units(100.0, 6);
        for (c, a) in [
            (deposit_call(&comet(), amt, owner), deposit_call(&aave(), amt, owner)),
            (withdraw_call(&comet(), amt, owner), withdraw_call(&aave(), amt, owner)),
        ] {
            assert_ne!(c.data[..4], a.data[..4]);
            assert_ne!(c.data, a.data);
        }
    }

    /// Same pinning as the withdraw overloads: a reordered or edited `sol!`
    /// block that changed these would silently encode a different call.
    #[test]
    fn comet_selectors_are_canonical() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        // supplyTo(address,address,uint256)
        assert_eq!(
            deposit_call(&comet(), amt, owner).data[..4],
            [0x42, 0x32, 0xcd, 0x63]
        );
        // withdrawTo(address,address,uint256)
        assert_eq!(
            withdraw_call(&comet(), amt, owner).data[..4],
            [0xc3, 0xb3, 0x5a, 0x7e]
        );
    }

    /// Comet is its own spender: supply pulls via
    /// `transferFrom(msg.sender, comet, amount)`, so the approval goes to the
    /// asset with the Comet address as spender, exactly like every other venue.
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

    /// Comet credits and pays out the address named in the calldata, and both
    /// deposit and withdraw must name the user's own wallet.
    #[test]
    fn comet_names_the_owner_in_both_directions() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        for data in [
            deposit_call(&comet(), amt, owner).data,
            withdraw_call(&comet(), amt, owner).data,
        ] {
            assert!(data.windows(20).any(|w| w == owner.as_slice()));
            assert!(data.windows(20).any(|w| w == comet().asset.as_slice()));
        }
    }

    #[test]
    fn registry_loads_the_compound_v3_kind() {
        let path = temp_venues(
            "comet",
            r#"[{"id":"Base:compound-v3:0x57","kind":"compound_v3","chain_id":84532,
                 "target":"0x571621Ce60Cebb0c1D442B5afb38B1663C6Bf017",
                 "asset":"0x036CbD53842c5426634e7929541eC2318f3dCF7e",
                 "asset_decimals":6,"symbol":"USDC"}]"#,
        );
        let r = Registry::load(&path).expect("compound_v3 must deserialize");
        assert_eq!(r.get("Base:compound-v3:0x57").unwrap().kind, VenueKind::CompoundV3);
        std::fs::remove_file(path).ok();
    }
}
