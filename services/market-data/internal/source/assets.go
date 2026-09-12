package source

import "strings"

// Canonical deposit assets we are willing to hold in a basket.
// Extend these two tables to support a new asset or a new vault wrapper.
var (
	// tokenAddresses maps lowercase ERC-20 addresses to their canonical symbol.
	tokenAddresses = map[string]string{
		// --- Base ---
		"0x833589fcd6edb6e08f4c7c32d4f71b54bda02913": "USDC",
		"0xd9aaec86b65d86f6a7b5b1b0c42ffa531710b6ca": "USDbC",
		"0x50c5725949a6f0c72e6c4a641f24049a917db0cb": "DAI",
		"0x820c137fa70c8691f0e44dc420a5e53c168921dc": "USDS",
		"0x4200000000000000000000000000000000000006": "WETH",
		"0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452": "wstETH",
		"0x2ae3f1ec7f1f5012cfeab0185bfc7aa3cf0dec22": "cbETH",
		"0xb6fe221fe9eef5aba221c348ba20a1bf5e73624c": "rETH",
		"0x04c0599ae5a44757c0af6f9ec3b93da8976c150a": "weETH",
		"0xcbb7c0000ab88b473b1f5afd9ef808440eed33bf": "cbBTC",
		"0x60a3e35cc302bfa44cb288bc5a4f316fdb1adb42": "EURC",
		"0x940181a94a35a4569e4529a3cdfb74e38fd98631": "AERO",
		"0x6bb7a212910682dcfdbd5bcbb3e28fb4e8da10ee": "GHO",
		"0x236aa50979d5f3de3bd1eeb40e81137f22ab794b": "tBTC",
		"0xecac9c5f704e954931349da37f60e39f515c11c1": "LBTC",
		"0x2416092f143378750bb29b79ed961ab195cceea5": "ezETH",
		// Yield-bearing assets that earn with no protocol interaction. They are
		// listed so a `hold` venue in them resolves; whether they can be VALUED
		// is a separate question the Pricer answers, and today WRSETH, SUSDS and
		// SYRUPUSDC have no Chainlink USD path on Base, so they surface in
		// /sources as unpriceable rather than being dropped silently.
		"0xedfa23602d0ec14714057867a78d01e94176bea0": "wrsETH",
		"0x5875eee11cf8398102fdad704c9e96607675467a": "sUSDS",
		"0x660975730059246a68521a3e2fbd4740173100f5": "syrupUSDC",
		// --- Base Sepolia (chain 84532) ---
		// Canonical Circle USDC; both testnet venues use this same token.
		// WETH is 0x4200...06 on both networks, so it needs no entry.
		"0x036cbd53842c5426634e7929541ec2318f3dcf7e": "USDC",
		// --- Ethereum (used if ETH is added to CHAINS) ---
		"0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48": "USDC",
		"0xdac17f958d2ee523a2206206994597c13d831ec7": "USDT",
		"0x6b175474e89094c44da98b954eedeac495271d0f": "DAI",
		"0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2": "WETH",
		"0x7f39c581f595b53c5cb19bd0b3f8da6c935e2ca0": "wstETH",
		"0x2260fac5e5542a773aa44fbcfedf7c193bc2c599": "WBTC",
	}

	// knownAssets is the set of canonical symbols accepted when the address
	// lookup misses. It is not a suffix table — see ResolveAsset.
	knownAssets = []string{
		"syrupUSDC", "USDbC", "USDC", "USDT", "sUSDS", "USDS", "DAI", "EURC", "GHO",
		"wstETH", "cbETH", "weETH", "wrsETH", "ezETH", "rETH", "WETH", "ETH",
		"cbBTC", "WBTC", "tBTC", "LBTC", "AERO",
	}

	// canonicalSymbols maps the UPPER-CASE lookup key to the canonical
	// spelling. It is knownAssets plus every symbol the address table can
	// produce, so adding an address also teaches the symbol path about it and
	// the two tables cannot drift apart.
	canonicalSymbols = func() map[string]string {
		out := make(map[string]string, len(knownAssets)+len(tokenAddresses))
		for _, a := range knownAssets {
			out[strings.ToUpper(a)] = a
		}
		for _, a := range tokenAddresses {
			out[strings.ToUpper(a)] = a
		}
		return out
	}()
)

// ResolveAsset determines the canonical deposit asset for a pool: an address
// hit, or a case-insensitive symbol match. Empty means "unknown" — callers must
// drop the venue rather than guess.
//
// The answer is spelled the way the TOKEN spells itself on chain — "wstETH",
// not "WSTETH". That spelling is what crosses the wire: it lands in a venue's
// `asset`, in the basket weights the client builds from /assets, in the
// executor's RouteRequest.asset, and in the allowlist symbol gen-venues emits
// from symbol(). The executor compares all of those with `!=`, and the swap
// allowlist spells tokens the same way, so one upper-cased letter anywhere in
// that chain makes a venue unroutable on entry AND on exit.
//
// UPPER CASE remains the internal LOOKUP KEY, and only that: the Chainlink feed
// tables, the apy:<chain>:<ASSET> zsets and isStable all upper-case whatever
// they are handed before looking it up. Nothing downstream may compare an asset
// case-sensitively against a literal.
//
// This used to fall back to longest-suffix matching, which is the guess the
// line above forbids. Every liquid-staking and restaking token ends in "ETH":
// wrsETH, ezETH, weETH, cbETH, wstETH. Each of those resolved correctly only
// while its address happened to be in tokenAddresses, and any venue whose
// underlying address was missing silently became plain ETH — wstETH trades
// around 1.2 ETH, so that is a ~20% mispricing, and the venue would show up in
// a basket as ETH exposure when it is a different instrument with different
// risk. The same trap applies to BTC wrappers, and to vault receipts
// (bbqUSDC, steakUSDC) whose ticker says USDC while the token is a share in
// something else.
//
// Adding the one missing address would have left the trap armed for the next
// token, so the suffix path is gone. The cost is losing a venue we cannot
// identify; the alternative cost is a user's money priced off the wrong asset.
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
	if a, ok := canonicalSymbols[strings.ToUpper(strings.TrimSpace(symbol))]; ok {
		return a
	}
	return ""
}

// stableAssets marks the canonical assets we treat as dollar/euro pegged.
// Keys are UPPER CASE because this is a lookup, not a wire value; isStable
// upper-cases its argument.
var stableAssets = map[string]bool{
	"USDC": true, "USDBC": true, "USDT": true, "USDS": true,
	"DAI": true, "EURC": true, "GHO": true,
	// Yield-bearing dollars. Still dollar-denominated exposure, so the basket
	// rules treat them as stable; their price is NOT 1.0 and is never assumed.
	"SUSDS": true, "SYRUPUSDC": true,
}

func isStable(asset string) bool { return stableAssets[strings.ToUpper(asset)] }
