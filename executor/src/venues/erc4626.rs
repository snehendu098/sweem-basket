use alloy_primitives::{Address, U256};
use alloy_sol_types::{sol, SolCall};

use super::{Call, Venue};

// Selectors are pinned by the tests in this file: reordering this block
// silently swaps calldata between protocols.
sol! {
    function deposit(uint256 assets, address receiver) external returns (uint256);
    function withdraw(uint256 assets, address receiver, address owner) external returns (uint256);
}

pub fn deposit(venue: &Venue, amount: U256, owner: Address) -> Result<Call, String> {
    Ok(Call {
        to: venue.target,
        data: depositCall {
            assets: amount,
            receiver: owner,
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
            assets: amount,
            receiver: owner,
            owner,
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

    fn vault() -> super::Venue {
        test_venue(VenueKind::Erc4626)
    }

    #[test]
    fn selectors_are_canonical() {
        let owner = Address::repeat_byte(0x33);
        let amt = U256::from(1u64);
        assert_eq!(
            deposit_call(&vault(), amt, owner).unwrap().data[..4],
            [0x6e, 0x55, 0x3f, 0x65]
        );
        assert_eq!(
            withdraw_call(&vault(), amt, owner).unwrap().data[..4],
            [0xb4, 0x60, 0xaf, 0x94]
        );
        assert_eq!(deposit_call(&vault(), amt, owner).unwrap().to, vault().target);
    }
}
