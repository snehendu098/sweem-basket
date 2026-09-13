use alloy_primitives::{Address, U256};
use alloy_sol_types::{sol, SolCall};

use super::{Call, Venue};

// Selectors are pinned by the tests in this file: reordering this block
// silently swaps calldata between protocols.
sol! {
    function supply(address asset, uint256 amount, address onBehalfOf, uint16 referralCode) external;
    function withdraw(address asset, uint256 amount, address to) external returns (uint256);
}

pub fn deposit(venue: &Venue, amount: U256, owner: Address) -> Result<Call, String> {
    Ok(Call {
        to: venue.target,
        data: supplyCall {
            asset: venue.asset,
            amount,
            onBehalfOf: owner,
            referralCode: 0,
        }
        .abi_encode()
        .into(),
        expect: None,
    })
}

pub fn withdraw(venue: &Venue, amount: U256, owner: Address) -> Result<Call, String> {
    Ok(Call {
        to: venue.target,
        data: withdrawCall {
            asset: venue.asset,
            amount,
            to: owner,
        }
        .abi_encode()
        .into(),
        expect: None,
    })
}

#[cfg(test)]
mod tests {
    use super::super::{deposit_call, test_venue, withdraw_call, VenueKind};
    use alloy_primitives::{Address, U256};

    fn aave() -> super::Venue {
        test_venue(VenueKind::AaveV3)
    }

    #[test]
    fn selectors_are_canonical() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        assert_eq!(
            deposit_call(&aave(), amt, owner).unwrap().data[..4],
            [0x61, 0x7b, 0xa0, 0x37]
        );
        assert_eq!(
            withdraw_call(&aave(), amt, owner).unwrap().data[..4],
            [0x69, 0x32, 0x8d, 0xec]
        );
        assert_eq!(deposit_call(&aave(), amt, owner).unwrap().to, aave().target);
    }
}
