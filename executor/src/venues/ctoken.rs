use alloy_primitives::{Address, U256};
use alloy_sol_types::{sol, SolCall};

use super::{Call, ExpectedEvent, Venue, TOPIC_MINT, TOPIC_REDEEM};

// Selectors are pinned by the tests in this file: reordering this block
// silently swaps calldata between protocols.
sol! {
    function mint(uint256 mintAmount) external returns (uint256);
    function redeemUnderlying(uint256 redeemAmount) external returns (uint256);
}

pub fn deposit(venue: &Venue, amount: U256, _owner: Address) -> Result<Call, String> {
    Ok(Call {
        to: venue.target,
        data: mintCall { mintAmount: amount }.abi_encode().into(),
        expect: Some(ExpectedEvent {
            address: venue.target,
            topic0: TOPIC_MINT,
        }),
    })
}

pub fn withdraw(venue: &Venue, amount: U256, _owner: Address) -> Result<Call, String> {
    Ok(Call {
        to: venue.target,
        data: redeemUnderlyingCall {
            redeemAmount: amount,
        }
        .abi_encode()
        .into(),
        expect: Some(ExpectedEvent {
            address: venue.target,
            topic0: TOPIC_REDEEM,
        }),
    })
}

#[cfg(test)]
mod tests {
    use super::super::{
        approve_call, deposit_call, test_venue, withdraw_call, VenueKind, TOPIC_MINT, TOPIC_REDEEM,
    };
    use alloy_primitives::{Address, U256};

    fn mtoken() -> super::Venue {
        test_venue(VenueKind::CToken)
    }

    fn vault() -> super::Venue {
        test_venue(VenueKind::Erc4626)
    }

    fn aave() -> super::Venue {
        test_venue(VenueKind::AaveV3)
    }

    fn comet() -> super::Venue {
        test_venue(VenueKind::CompoundV3)
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
}
