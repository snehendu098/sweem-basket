package source

import (
	"strings"
	"testing"
)

const usdcBase = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"

func TestResolveAsset(t *testing.T) {
	tests := []struct {
		name       string
		underlying []string
		symbol     string
		want       string
	}{
		{"address wins over symbol", []string{usdcBase}, "STEAKUSDC", "USDC"},
		{"address is case insensitive", []string{"0X833589FCD6EDB6E08F4C7C32D4F71B54BDA02913"}, "junk", "USDC"},
		{"base sepolia usdc by address", []string{"0x036CbD53842c5426634e7929541eC2318f3dCF7e"}, "aBasSepUSDC", "USDC"},
		{"weth by address", []string{"0x4200000000000000000000000000000000000006"}, "MWETH", "WETH"},
		{"symbol fallback, plain", nil, "USDC", "USDC"},
		// The answer is always the token's own spelling, whatever case the
		// caller asked in: that string crosses the wire to the executor, which
		// compares it with != against venues.json and swaps.json.
		{"answer is the on-chain spelling, not the query's", nil, "WSTETH", "wstETH"},
		{"lower-case query, same answer", nil, "wsteth", "wstETH"},
		{"exact symbol, case insensitive", nil, "WSTETH", "wstETH"},
		{"exact symbol, plain weth", nil, "weth", "WETH"},
		// A vault receipt is not its underlying: bbqUSDC is a share in something,
		// and pricing it as USDC is the guess this function refuses to make.
		{"vault receipt is not guessed", nil, "bbqUSDC", ""},
		{"aToken receipt is not guessed", nil, "aBasWETH", ""},
		{"usdbc not usdc", nil, "USDbC", "USDbC"},
		{"cbbtc", nil, "cbbtc", "cbBTC"},
		{"unknown single token", []string{"0x000000000000000000000000000000000000dead"}, "XYZ", ""},
		{"unknown symbol", nil, "PEPE", ""},
		{"multi token, none known: no guess", []string{"0xdead", "0xbeef"}, "USDC-WETH", ""},
		{"multi token, first known", []string{"0xdead", usdcBase}, "USDC-WETH", "USDC"},
		{"empty", nil, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveAsset(tc.underlying, tc.symbol); got != tc.want {
				t.Errorf("ResolveAsset(%v, %q) = %q, want %q", tc.underlying, tc.symbol, got, tc.want)
			}
		})
	}
}

// Every liquid-staking and restaking token ends in "ETH". Under the old
// longest-suffix fallback each of these resolved to plain ETH the moment its
// address was missing from the table — wstETH trades ~1.2 ETH, so that was a
// ~20% mispricing on an instrument with different risk, wearing ETH's label.
// Nothing may resolve to ETH unless it genuinely is ETH or WETH.
func TestNoEthDerivativeResolvesToPlainEth(t *testing.T) {
	tests := []struct {
		symbol string
		want   string
	}{
		{"wrsETH", "wrsETH"}, // in the table today
		{"ezETH", "ezETH"},
		{"weETH", "weETH"},
		{"cbETH", "cbETH"},
		{"wstETH", "wstETH"},
		{"WETH", "WETH"},
		{"ETH", "ETH"},
		// Not in any table: must be unknown, never ETH.
		{"pufETH", ""},
		{"rsETH", ""},
		{"someNewLstETH", ""},
		{"XYZ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.symbol, func(t *testing.T) {
			got := ResolveAsset(nil, tc.symbol)
			if got != tc.want {
				t.Fatalf("ResolveAsset(nil, %q) = %q, want %q", tc.symbol, got, tc.want)
			}
			if got == "ETH" && !strings.EqualFold(tc.symbol, "ETH") && !strings.EqualFold(tc.symbol, "WETH") {
				t.Fatalf("%q resolved to plain ETH", tc.symbol)
			}
		})
	}
}

// The same trap on the BTC side: a wrapper is not the root asset.
func TestNoBtcWrapperResolvesToPlainBtc(t *testing.T) {
	for _, sym := range []string{"solvBTC", "pumpBTC", "eBTC"} {
		if got := ResolveAsset(nil, sym); got != "" {
			t.Fatalf("ResolveAsset(nil, %q) = %q, want unknown", sym, got)
		}
	}
}

// Any asset ResolveAsset can name must have a family decision on file, or
// gen-venues fails on it the first time it shows up in a market.
func TestEveryKnownTokenHasAFamilyDecision(t *testing.T) {
	for addr, sym := range tokenAddresses {
		if _, decided := FamilyOfAddress(addr); !decided {
			t.Errorf("%s (%s) has no assetFamilies entry", sym, addr)
		}
	}
}

// The families are a substitution claim, not a price-feed quote currency.
// ezETH and wrsETH quote against ETH and carry slashing risk ETH does not.
func TestFamiliesAreSubstitutionNotQuoteCurrency(t *testing.T) {
	for _, sym := range []string{"WETH", "wstETH", "cbETH", "weETH", "rETH", "wrsETH"} {
		if f, _ := FamilyOf(sym); f != "ETH" {
			t.Errorf("FamilyOf(%s) = %q, want ETH", sym, f)
		}
	}
	for _, sym := range []string{"cbBTC", "WBTC"} {
		if f, _ := FamilyOf(sym); f != "BTC" {
			t.Errorf("FamilyOf(%s) = %q, want BTC", sym, f)
		}
	}
	for _, sym := range []string{"USDC", "USDbC", "USDS", "GHO", "DAI", "USDT"} {
		if f, _ := FamilyOf(sym); f != "USD" {
			t.Errorf("FamilyOf(%s) = %q, want USD", sym, f)
		}
	}
	if f, _ := FamilyOf("EURC"); f != "EUR" {
		t.Errorf("EURC is not USD: got %q", f)
	}
	for _, sym := range []string{"ezETH", "tBTC", "LBTC", "AERO", "sUSDS", "syrupUSDC"} {
		f, decided := FamilyOf(sym)
		if !decided || f != FamilyNone {
			t.Errorf("FamilyOf(%s) = %q decided=%v, want a decided singleton", sym, f, decided)
		}
	}
}
