package source

import "strings"

// Canonical deposit assets we are willing to hold in a basket.
// Extend these two tables to support a new asset or a new vault wrapper.
var (
	// tokenAddresses maps lowercase ERC-20 addresses to their canonical symbol.
	tokenAddresses = map[string]string{
		// --- Base ---
		"0x833589fcd6edb6e08f4c7c32d4f71b54bda02913": "USDC",
		"0xd9aaec86b65d86f6a7b5b1b0c42ffa531710b6ca": "USDBC",
		"0x50c5725949a6f0c72e6c4a641f24049a917db0cb": "DAI",
		"0x820c137fa70c8691f0e44dc420a5e53c168921dc": "USDS",
		"0x4200000000000000000000000000000000000006": "WETH",
		"0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452": "WSTETH",
		"0x2ae3f1ec7f1f5012cfeab0185bfc7aa3cf0dec22": "CBETH",
		"0xb6fe221fe9eef5aba221c348ba20a1bf5e73624c": "RETH",
		"0x04c0599ae5a44757c0af6f9ec3b93da8976c150a": "WEETH",
		"0xcbb7c0000ab88b473b1f5afd9ef808440eed33bf": "CBBTC",
		"0x60a3e35cc302bfa44cb288bc5a4f316fdb1adb42": "EURC",
		"0x940181a94a35a4569e4529a3cdfb74e38fd98631": "AERO",
		"0x6bb7a212910682dcfdbd5bcbb3e28fb4e8da10ee": "GHO",
		"0x236aa50979d5f3de3bd1eeb40e81137f22ab794b": "TBTC",
		"0xecac9c5f704e954931349da37f60e39f515c11c1": "LBTC",
		"0x2416092f143378750bb29b79ed961ab195cceea5": "EZETH",
		// --- Base Sepolia (chain 84532) ---
		// Canonical Circle USDC; both testnet venues use this same token.
		// WETH is 0x4200...06 on both networks, so it needs no entry.
		"0x036cbd53842c5426634e7929541ec2318f3dcf7e": "USDC",
		// --- Ethereum (used if ETH is added to CHAINS) ---
		"0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48": "USDC",
		"0xdac17f958d2ee523a2206206994597c13d831ec7": "USDT",
		"0x6b175474e89094c44da98b954eedeac495271d0f": "DAI",
		"0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2": "WETH",
		"0x7f39c581f595b53c5cb19bd0b3f8da6c935e2ca0": "WSTETH",
		"0x2260fac5e5542a773aa44fbcfedf7c193bc2c599": "WBTC",
	}

	// knownAssets is the symbol fallback set, longest-suffix wins so that
	// vault receipts (STEAKUSDC, bbqUSDC, ...) resolve to their underlying.
	knownAssets = []string{
		"USDBC", "USDC", "USDT", "USDS", "DAI", "EURC", "GHO",
		"WSTETH", "CBETH", "WEETH", "RETH", "WETH", "ETH",
		"CBBTC", "WBTC", "TBTC", "LBTC", "AERO",
	}
)

// ResolveAsset determines the canonical deposit asset for a pool.
// Address lookup wins; symbol suffix matching is the fallback.
// Empty result means "unknown" — callers must drop the venue rather than guess.
func ResolveAsset(underlying []string, symbol string) string {
	for _, addr := range underlying {
		if a, ok := tokenAddresses[strings.ToLower(strings.TrimSpace(addr))]; ok {
			return a
		}
	}
	// Multi-token pools with no recognised token: refuse to guess from the symbol.
	if len(underlying) > 1 {
		return ""
	}
	s := strings.ToUpper(strings.TrimSpace(symbol))
	best := ""
	for _, a := range knownAssets {
		if strings.HasSuffix(s, a) && len(a) > len(best) {
			best = a
		}
	}
	return best
}

// stableAssets marks the canonical assets we treat as dollar/euro pegged.
var stableAssets = map[string]bool{
	"USDC": true, "USDBC": true, "USDT": true, "USDS": true,
	"DAI": true, "EURC": true, "GHO": true,
}

func isStable(asset string) bool { return stableAssets[asset] }
