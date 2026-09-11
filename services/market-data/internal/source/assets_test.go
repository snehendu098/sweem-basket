package source

import "testing"

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
		{"symbol fallback, vault prefix", nil, "bbqUSDC", "USDC"},
		{"symbol fallback, longest suffix wins", nil, "wstETH", "WSTETH"},
		{"symbol fallback, weth over eth", nil, "aBasWETH", "WETH"},
		{"usdbc not usdc", nil, "USDbC", "USDBC"},
		{"cbbtc", nil, "cbBTC", "CBBTC"},
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
