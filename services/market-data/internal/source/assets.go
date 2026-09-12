package source

import (
	"sort"
	"strings"

	"github.com/snehendu098/sweem-basket/internal/shared/prices"
)

var (
	tokenAddresses = map[string]string{
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
		"0xedfa23602d0ec14714057867a78d01e94176bea0": "wrsETH",
		"0x5875eee11cf8398102fdad704c9e96607675467a": "sUSDS",
		"0x660975730059246a68521a3e2fbd4740173100f5": "syrupUSDC",
		"0x036cbd53842c5426634e7929541ec2318f3dcf7e": "USDC",
		"0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48": "USDC",
		"0xdac17f958d2ee523a2206206994597c13d831ec7": "USDT",
		"0x6b175474e89094c44da98b954eedeac495271d0f": "DAI",
		"0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2": "WETH",
		"0x7f39c581f595b53c5cb19bd0b3f8da6c935e2ca0": "wstETH",
		"0x2260fac5e5542a773aa44fbcfedf7c193bc2c599": "WBTC",
	}

	knownAssets = []string{
		"syrupUSDC", "USDbC", "USDC", "USDT", "sUSDS", "USDS", "DAI", "EURC", "GHO",
		"wstETH", "cbETH", "weETH", "wrsETH", "ezETH", "rETH", "WETH", "ETH",
		"cbBTC", "WBTC", "tBTC", "LBTC", "AERO",
	}

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

func ResolveAsset(underlying []string, symbol string) string {
	for _, addr := range underlying {
		if a, ok := tokenAddresses[strings.ToLower(strings.TrimSpace(addr))]; ok {
			return a
		}
	}
	if len(underlying) > 1 {
		return ""
	}
	if a, ok := canonicalSymbols[strings.ToUpper(strings.TrimSpace(symbol))]; ok {
		return a
	}
	return ""
}

var stableAssets = map[string]bool{
	"USDC": true, "USDBC": true, "USDT": true, "USDS": true,
	"DAI": true, "EURC": true, "GHO": true,
	"SUSDS": true, "SYRUPUSDC": true,
}

func isStable(asset string) bool { return stableAssets[strings.ToUpper(asset)] }

// A family is a set of instruments a router may substitute for one another.
// Decided per address by a human, never derived: wrsETH and ezETH quote
// against ETH and are NOT ETH, and LINK is nobody's substitute.
const FamilyNone = ""

var assetFamilies = map[string]string{
	// USD
	"0x833589fcd6edb6e08f4c7c32d4f71b54bda02913": "USD", // USDC
	"0x036cbd53842c5426634e7929541ec2318f3dcf7e": "USD", // USDC, base-sepolia
	"0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48": "USD", // USDC, ethereum
	"0xd9aaec86b65d86f6a7b5b1b0c42ffa531710b6ca": "USD", // USDbC
	"0x820c137fa70c8691f0e44dc420a5e53c168921dc": "USD", // USDS
	"0x6bb7a212910682dcfdbd5bcbb3e28fb4e8da10ee": "USD", // GHO
	"0x50c5725949a6f0c72e6c4a641f24049a917db0cb": "USD", // DAI
	"0x6b175474e89094c44da98b954eedeac495271d0f": "USD", // DAI, ethereum
	"0xdac17f958d2ee523a2206206994597c13d831ec7": "USD", // USDT
	"0x5d3a1ff2b6bab83b63cd9ad0787074081a52ef34": "USD", // USDe
	// EUR
	"0x60a3e35cc302bfa44cb288bc5a4f316fdb1adb42": "EUR", // EURC
	// ETH
	"0x4200000000000000000000000000000000000006": "ETH", // WETH
	"0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2": "ETH", // WETH, ethereum
	"0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452": "ETH", // wstETH
	"0x7f39c581f595b53c5cb19bd0b3f8da6c935e2ca0": "ETH", // wstETH, ethereum
	"0x2ae3f1ec7f1f5012cfeab0185bfc7aa3cf0dec22": "ETH", // cbETH
	"0x04c0599ae5a44757c0af6f9ec3b93da8976c150a": "ETH", // weETH
	"0xb6fe221fe9eef5aba221c348ba20a1bf5e73624c": "ETH", // rETH
	"0xedfa23602d0ec14714057867a78d01e94176bea0": "ETH", // wrsETH
	// BTC
	"0xcbb7c0000ab88b473b1f5afd9ef808440eed33bf": "BTC", // cbBTC
	"0x2260fac5e5542a773aa44fbcfedf7c193bc2c599": "BTC", // WBTC
	// Singletons: no substitute exists, so no family.
	"0x2416092f143378750bb29b79ed961ab195cceea5": FamilyNone, // ezETH, restaking
	"0x236aa50979d5f3de3bd1eeb40e81137f22ab794b": FamilyNone, // tBTC
	"0xecac9c5f704e954931349da37f60e39f515c11c1": FamilyNone, // LBTC
	"0x5875eee11cf8398102fdad704c9e96607675467a": FamilyNone, // sUSDS
	"0x660975730059246a68521a3e2fbd4740173100f5": FamilyNone, // syrupUSDC
	"0x940181a94a35a4569e4529a3cdfb74e38fd98631": FamilyNone, // AERO
	"0x88fb150bdc53a65fe94dea0c9ba0a6daf8c6e196": FamilyNone, // LINK
	"0x63706e401c06ac8513145b7687a14804d17f814b": FamilyNone, // AAVE
	"0xbaa5cc21fd487b8fcc2f632f3f4e8d37262a0842": FamilyNone, // MORPHO
	"0x0b3e328455c4059eeb9e3f84b5543f74e24e7e1b": FamilyNone, // VIRTUAL
	"0xacfe6019ed1a7dc6f7b508c02d1b04ec88cc21bf": FamilyNone, // VVV
}

var familyBySymbol = func() map[string]string {
	out := make(map[string]string, len(assetFamilies))
	for addr, fam := range assetFamilies {
		if sym, ok := tokenAddresses[addr]; ok {
			out[strings.ToUpper(sym)] = fam
		}
	}
	out["ETH"] = "ETH"
	return out
}()

// FamilyOfAddress reports the family and whether anyone has decided one.
func FamilyOfAddress(addr string) (string, bool) {
	f, ok := assetFamilies[strings.ToLower(strings.TrimSpace(addr))]
	return f, ok
}

func FamilyOf(symbol string) (string, bool) {
	f, ok := familyBySymbol[strings.ToUpper(strings.TrimSpace(symbol))]
	return f, ok
}

// Assets a user can hold on this chain whether or not a lending market exists
// for them: someone decided their family and this chain can price them.
func HoldableAssets(chainID int) []string {
	feeds, ratios := prices.FeedsFor(chainID)
	seen := map[string]bool{}
	out := []string{}
	for addr := range assetFamilies {
		sym, ok := tokenAddresses[addr]
		if !ok || seen[sym] {
			continue
		}
		key := strings.ToUpper(sym)
		if _, direct := feeds[key]; !direct {
			if _, composed := ratios[key]; !composed {
				continue
			}
		}
		seen[sym] = true
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}
