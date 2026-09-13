use alloy_primitives::{Address, U256};
use alloy_sol_types::{sol, SolCall};

use super::{Call, Venue};

// Selectors are pinned by the tests in this file: reordering this block
// silently swaps calldata between protocols.
sol! {
    function supplyTo(address dst, address asset, uint256 amount) external;
    function withdrawTo(address to, address asset, uint256 amount) external;
}

pub fn deposit(venue: &Venue, amount: U256, owner: Address) -> Result<Call, String> {
    Ok(Call {
        to: venue.target,
        data: supplyToCall {
            dst: owner,
            asset: venue.asset,
            amount,
        }
        .abi_encode()
        .into(),
        expect: None,
    })
}

pub fn withdraw(venue: &Venue, amount: U256, owner: Address) -> Result<Call, String> {
    Ok(Call {
        to: venue.target,
        data: withdrawToCall {
            to: owner,
            asset: venue.asset,
            amount,
        }
        .abi_encode()
        .into(),
        expect: None,
    })
}

#[cfg(test)]
mod tests {
    use super::super::{approve_call, deposit_call, test_venue, usd_to_units, withdraw_call, VenueKind};
    use alloy_primitives::{Address, U256};

    fn comet() -> super::Venue {
        test_venue(VenueKind::CompoundV3)
    }

    fn aave() -> super::Venue {
        test_venue(VenueKind::AaveV3)
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
}
