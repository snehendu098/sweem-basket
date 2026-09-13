use alloy_primitives::{Address, U256};

use super::{Call, Venue};

pub fn deposit(venue: &Venue, _amount: U256, _owner: Address) -> Result<Call, String> {
    Err(hold_has_no_call(venue, "deposit"))
}

pub fn withdraw(venue: &Venue, _amount: U256, _owner: Address) -> Result<Call, String> {
    Err(hold_has_no_call(venue, "withdraw"))
}

fn hold_has_no_call(venue: &Venue, action: &str) -> String {
    format!(
        "venue {} is a hold position: there is no {action} call, it is entered and exited by swapping {}",
        venue.id, venue.symbol
    )
}

#[cfg(test)]
mod tests {
    use super::super::{deposit_call, test_venue, withdraw_call, Venue, VenueKind};
    use alloy_primitives::{Address, U256};

    #[test]
    fn hold_has_no_deposit_or_withdraw_call() {
        let hold = Venue {
            symbol: "wstETH".into(),
            ..test_venue(VenueKind::Hold)
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
}
