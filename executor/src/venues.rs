use alloy_primitives::{Address, Bytes, U256};
use alloy_sol_types::{sol, SolCall};
use serde::{Deserialize, Serialize};
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

    // Compound v2 fork (Moonwell mTokens). Both RETURN an error code rather
    // than reverting on several failure paths — see VenueKind::CToken.
    function mint(uint256 mintAmount) external returns (uint256);
    function redeemUnderlying(uint256 redeemAmount) external returns (uint256);

    // ERC-20
    function approve(address spender, uint256 amount) external returns (bool);
}

/// How a venue's deposit/withdraw calldata is shaped. Adding a protocol means
/// adding a variant here and a match arm below — nothing else changes.
#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum VenueKind {
    /// Standard tokenized vault. One adapter covers most of Base's yield.
    Erc4626,
    /// Aave v3 Pool: supply/withdraw take the asset address explicitly.
    AaveV3,
    /// Compound v3 (Comet). One contract per base asset, and it is its own
    /// spender: `supply` pulls with `transferFrom(msg.sender, comet, amount)`.
    CompoundV3,
    /// Compound v2 fork: Moonwell's mTokens. Two properties make this kind
    /// different from every other one here, both verified by `eth_call` against
    /// the deployed mUSDC market on Base (`0xEdc817A2…`, `isMToken() == true`):
    ///
    /// 1. **Failures are return values, not reverts.** `redeemUnderlying(1e6)`
    ///    from an account with no position returns `9` (MATH_ERROR) in a
    ///    *successful* transaction. Receipt status alone would call that a
    ///    confirmed withdrawal of money that never moved, so every ctoken call
    ///    additionally requires its success event (`Mint` / `Redeem`) in the
    ///    receipt logs. Some failures do revert (`mint` on a paused market
    ///    reverts with "mint is paused"; a missing allowance reverts inside
    ///    USDC) — the event check covers both shapes.
    /// 2. **There is no recipient parameter.** `mint`/`redeemUnderlying` credit
    ///    `msg.sender`, and the deployed contract exposes no `mintTo`-style
    ///    variant, so the destination cannot be asserted from the calldata the
    ///    way it can for ERC-4626, Aave and Comet. It is correct only because
    ///    the executor sends from the user's own wallet and never from one of
    ///    its own — that invariant is doing the work here.
    ///
    /// The wire name is `ctoken`, not serde's snake_case `c_token`: it has to
    /// match what the generator writes and what the README documents.
    #[serde(rename = "ctoken")]
    CToken,
    /// No protocol at all: the asset earns by appreciating, so holding it in the
    /// user's own wallet *is* the position (wstETH, cbETH, weETH). There is no
    /// contract to call, which is why `deposit_call`/`withdraw_call` refuse this
    /// kind rather than encoding something — entering is a swap into the token
    /// and exiting is a swap back out, both handled by the swap allowlist.
    Hold,
}

/// Event a call must emit to count as having actually done anything.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ExpectedEvent {
    pub address: Address,
    pub topic0: &'static str,
}

/// `Mint(address,uint256,uint256)` — keccak of the signature, confirmed against
/// live logs on the Base mUSDC market.
pub const TOPIC_MINT: &str = "0x4c209b5fc8ad50758f13e2e1088ba56a560dff690a1c6fef26394f4c03821c4f";
/// `Redeem(address,uint256,uint256)` — same verification.
pub const TOPIC_REDEEM: &str = "0xe5b754fb1abb7f01b499791d0b820ae3b6af3424ac1c59768edb53f4ec31a929";

#[derive(Clone, Debug, Deserialize, Serialize)]
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

/// Canonical chain label for a chain id, matching market-data's venue ids and
/// the `chains` package on the Go side. `None` means a chain this executor does
/// not serve, which is a configuration error rather than a runtime condition.
pub fn chain_label(chain_id: u64) -> Option<&'static str> {
    match chain_id {
        8453 => Some("base"),
        84532 => Some("base-sepolia"),
        _ => None,
    }
}

/// The allowlist. A venue absent from here cannot be transacted with, no matter
/// what the wallet service asks for.
#[derive(Clone, Debug, Default)]
pub struct Registry(HashMap<String, Venue>);

/// The allowlist file. It is generated (see
/// `services/market-data/cmd/gen-venues`), so it carries provenance alongside
/// the rows; a bare array is still accepted because a hand-written file is a
/// legitimate thing to point `VENUES_PATH` at in a test.
#[derive(Deserialize)]
struct GeneratedFile {
    venues: Vec<Venue>,
}

/// Parsed without an untagged enum on purpose: untagged variants report only
/// "data did not match any variant", which hides which field of which venue was
/// wrong — and this file is the security boundary, so its parse errors have to
/// name the problem.
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
            // An empty allowlist refuses every request. That is a misconfiguration
            // wearing the costume of a code bug, so fail loudly at startup instead.
            return Err(format!("venues {path} is empty: nothing would be routable"));
        }
        // The id carries the chain label, and the wallet service routes by id.
        // A label that disagrees with chain_id would send a Sepolia venue down a
        // mainnet route or the reverse, so it is a startup failure, not a
        // runtime surprise.
        for v in &venues {
            let want = chain_label(v.chain_id)
                .ok_or_else(|| format!("venue {}: unsupported chain_id {}", v.id, v.chain_id))?;
            if !v.id.starts_with(&format!("{want}:")) {
                return Err(format!(
                    "venue {}: id must start with the chain label {want:?} for chain_id {}",
                    v.id, v.chain_id
                ));
            }
            // A hold venue has no target contract — nothing is ever called on it —
            // so the usual "target is not the asset" check has nothing to check.
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

    /// Every allowlisted venue, sorted by id so the response is stable.
    ///
    /// This is what makes the router honest: the wallet service reads it and
    /// only ever proposes venues that can actually be executed, instead of
    /// discovering at submission time that the best-rate venue is one this
    /// process cannot encode calldata for.
    pub fn all(&self) -> Vec<&Venue> {
        let mut out: Vec<&Venue> = self.0.values().collect();
        out.sort_by(|a, b| a.id.cmp(&b.id));
        out
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
    /// Set when a successful receipt is not sufficient evidence the call did
    /// anything — see VenueKind::CToken. `None` means status is enough.
    pub expect: Option<ExpectedEvent>,
}

/// Approval the vault needs before it can pull funds. Returned separately so the
/// caller can decide whether it is already in place.
pub fn approve_call(venue: &Venue, amount: U256) -> Call {
    approve(venue.asset, venue.target, amount)
}

/// Bare ERC-20 approval. Split out of `approve_call` because a swap leg
/// approves the Uniswap router, which is not a venue.
pub fn approve(token: Address, spender: Address, amount: U256) -> Call {
    Call {
        to: token,
        data: approveCall { spender, amount }.abi_encode().into(),
        expect: None,
    }
}

/// Encode a deposit of `amount` (in the asset's smallest unit) on behalf of `owner`.
///
/// Errors for `Hold`: there is no protocol call to make, and encoding one
/// against a venue with no target would send funds to an address that means
/// nothing. The caller routes a hold venue through the swap legs instead.
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
        // mint credits msg.sender: the user's own wallet, which is the only
        // account this executor ever sends from.
        VenueKind::CToken => mintCall { mintAmount: amount }.abi_encode(),
        VenueKind::Hold => return Err(hold_has_no_call(venue, "deposit")),
    };
    Ok(Call {
        to: venue.target,
        data: data.into(),
        expect: expected_event(venue, TOPIC_MINT),
    })
}

/// Encode a withdrawal of `amount` back to `owner`. Errors for `Hold`, for the
/// same reason `deposit_call` does.
pub fn withdraw_call(venue: &Venue, amount: U256, owner: Address) -> Result<Call, String> {
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
        // Denominated in the underlying, like every other arm here — the
        // mToken-denominated `redeem` is deliberately not used.
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

/// Only ctoken calls need event evidence; everything else reverts on failure,
/// which the receipt status already reports.
fn expected_event(venue: &Venue, topic0: &'static str) -> Option<ExpectedEvent> {
    match venue.kind {
        VenueKind::CToken => Some(ExpectedEvent {
            address: venue.target,
            topic0,
        }),
        _ => None,
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

    /// Same pinning as the other protocols: selectors are the canonical keccak
    /// prefixes, so a reordered or edited `sol!` block cannot silently swap
    /// calldata between protocols.
    #[test]
    fn ctoken_selectors_are_canonical() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        // mint(uint256)
        assert_eq!(
            deposit_call(&mtoken(), amt, owner).unwrap().data[..4],
            [0xa0, 0x71, 0x2d, 0x68]
        );
        // redeemUnderlying(uint256)
        assert_eq!(
            withdraw_call(&mtoken(), amt, owner).unwrap().data[..4],
            [0x85, 0x2a, 0x12, 0xe3]
        );
    }

    /// An mToken has no recipient parameter, so no other protocol's arm can be
    /// mistaken for it and vice versa.
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

    /// The guard that makes this kind safe to route into at all: a ctoken call
    /// is only believed when it emits its success event.
    #[test]
    fn ctoken_calls_demand_their_success_event() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        let d = deposit_call(&mtoken(), amt, owner).unwrap().expect.expect("mint must be verified");
        assert_eq!(d.address, mtoken().target);
        assert_eq!(d.topic0, TOPIC_MINT);
        let w = withdraw_call(&mtoken(), amt, owner).unwrap().expect.expect("redeem must be verified");
        assert_eq!(w.topic0, TOPIC_REDEEM);

        // Everything else reverts on failure, so status alone is evidence.
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

    /// The allowlist has to be readable, or the wallet service cannot filter
    /// its routing against it and goes back to proposing venues that will be
    /// refused at submission time.
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
            withdraw_call(&vault(), amt, owner).unwrap().data[..4],
            [0xb4, 0x60, 0xaf, 0x94]
        );
        // withdraw(address,uint256,address)
        assert_eq!(
            withdraw_call(&aave(), amt, owner).unwrap().data[..4],
            [0x69, 0x32, 0x8d, 0xec]
        );
    }

    #[test]
    fn deposit_selectors_are_canonical() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        // deposit(uint256,address)
        assert_eq!(
            deposit_call(&vault(), amt, owner).unwrap().data[..4],
            [0x6e, 0x55, 0x3f, 0x65]
        );
        // supply(address,uint256,address,uint16)
        assert_eq!(
            deposit_call(&aave(), amt, owner).unwrap().data[..4],
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

    /// The shipped allowlist must parse — a typo here is a startup failure in
    /// production, and the executor refuses to start half-configured.
    /// The shipped allowlist is generated (see
    /// `services/market-data/cmd/gen-venues`), so this asserts the invariants
    /// the generator claims rather than a fixed list of venues: a typo here is
    /// a startup failure in production.
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
            // The same token cannot have two different decimals: that would
            // mean one of the two entries is pointing at the wrong contract.
            if let Some(prev) = decimals_by_symbol.insert(&v.symbol, v.asset_decimals) {
                assert_eq!(prev, v.asset_decimals, "{} has conflicting decimals", v.symbol);
            }
        }
        // Both networks are served at once; the frontend picks per request.
        assert!(chains.contains(&8453), "Base mainnet venues missing");
        assert!(chains.contains(&84532), "Base Sepolia venues missing");
    }

    /// The generated file carries provenance; a bare array must still load so a
    /// test or a local experiment can point VENUES_PATH at a hand-written file.
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

    /// A venue id whose chain label disagrees with its chain_id is the exact
    /// mistake that leaks a testnet venue into a mainnet route. It must not load.
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

    /// Comet's supply is `(asset, amount)` while Aave's is
    /// `(asset, amount, onBehalfOf, referralCode)`. Encoding one as the other
    /// would put an amount where an address belongs, so the two must never
    /// share a selector.
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

    /// Same pinning as the withdraw overloads: a reordered or edited `sol!`
    /// block that changed these would silently encode a different call.
    #[test]
    fn comet_selectors_are_canonical() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        // supplyTo(address,address,uint256)
        assert_eq!(
            deposit_call(&comet(), amt, owner).unwrap().data[..4],
            [0x42, 0x32, 0xcd, 0x63]
        );
        // withdrawTo(address,address,uint256)
        assert_eq!(
            withdraw_call(&comet(), amt, owner).unwrap().data[..4],
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
            deposit_call(&comet(), amt, owner).unwrap().data,
            withdraw_call(&comet(), amt, owner).unwrap().data,
        ] {
            assert!(data.windows(20).any(|w| w == owner.as_slice()));
            assert!(data.windows(20).any(|w| w == comet().asset.as_slice()));
        }
    }

    /// A hold venue has no protocol call. Encoding one anyway would point
    /// calldata at a target that means nothing, so both directions must refuse
    /// — and refuse, not panic: the handler turns this into a 400.
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

    /// The wire name is `hold`, and such a row must load even though its target
    /// is not a distinct contract — there is no contract.
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
        // And it still inherits the chain-label check every other kind gets.
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
