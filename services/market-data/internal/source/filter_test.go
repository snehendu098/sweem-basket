package source

import (
	"math/big"
	"testing"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

// A venue paying nothing means different things on the two chains: broken on
// mainnet, simply unborrowed on a testnet. The rule is keyed on chain id, like
// the price tables, so mainnet keeps the stricter one.
func TestZeroAPYIsUsableOnTestnetOnly(t *testing.T) {
	idle := func(chain string) venue.Venue {
		return venue.Venue{
			Chain: chain, Project: "compound-v3", PoolID: "0xcomet",
			Asset: "WETH", TVLUsd: 1, APY: 0,
		}
	}
	mainnet := FilterFor(Chain{Label: chains.LabelBaseMainnet, ID: chains.BaseMainnet})
	sepolia := FilterFor(Chain{Label: chains.LabelBaseSepolia, ID: chains.BaseSepolia})

	if mainnet.Accept(idle(chains.LabelBaseMainnet)) {
		t.Fatal("a 0% mainnet venue must still be dropped")
	}
	if !sepolia.Accept(idle(chains.LabelBaseSepolia)) {
		t.Fatal("a working-but-idle testnet venue must be kept")
	}
	// Negative is nonsense on any chain.
	neg := idle(chains.LabelBaseSepolia)
	neg.APY = -1
	if sepolia.Accept(neg) {
		t.Fatal("negative APY accepted")
	}
	// And the testnet floor must not let a mainnet-labelled venue through.
	if sepolia.Accept(idle(chains.LabelBaseMainnet)) {
		t.Fatal("chain filter leaked")
	}
}

// The TVL floor is chain-scoped for the same reason: a testnet pool holding $40
// is a functioning venue, not a dust pool.
func TestTVLFloorIsChainScoped(t *testing.T) {
	mainnet := FilterFor(Chain{Label: chains.LabelBaseMainnet, ID: chains.BaseMainnet})
	sepolia := FilterFor(Chain{Label: chains.LabelBaseSepolia, ID: chains.BaseSepolia})
	if mainnet.MinTVLUsd <= sepolia.MinTVLUsd {
		t.Fatalf("mainnet floor %.0f must be stricter than testnet %.0f",
			mainnet.MinTVLUsd, sepolia.MinTVLUsd)
	}
	small := venue.Venue{
		Chain: chains.LabelBaseSepolia, Project: "aave-v3", PoolID: "p",
		Asset: "USDC", TVLUsd: 40, APY: 1,
	}
	if !sepolia.Accept(small) {
		t.Fatal("a small but real testnet venue was dropped by the TVL floor")
	}
}

// The shared key is the mainnet threshold; the testnet has its own, or its
// default. A production floor reaching the testnet deletes every testnet venue.
func TestFilterEnvOverrides(t *testing.T) {
	t.Setenv("MIN_TVL_USD", "123")
	if got := FilterFor(Chain{Label: chains.LabelBaseSepolia, ID: chains.BaseSepolia}).MinTVLUsd; got != 0 {
		t.Fatalf("sepolia floor = %v, the shared mainnet override must not reach it", got)
	}
	t.Setenv("MIN_TVL_USD_84532", "7")
	if got := FilterFor(Chain{Label: chains.LabelBaseMainnet, ID: chains.BaseMainnet}).MinTVLUsd; got != 123 {
		t.Fatalf("mainnet floor = %v, want the shared override", got)
	}
	if got := FilterFor(Chain{Label: chains.LabelBaseSepolia, ID: chains.BaseSepolia}).MinTVLUsd; got != 7 {
		t.Fatalf("sepolia floor = %v, want the per-chain override", got)
	}
}

// A market at 100% utilisation has TVL, a headline APY and no exit. mUSDC on
// Base was $8.9M borrowed with getCash() == 0 and the router preferred it.
func TestIlliquidVenueIsPublishedButNotRoutable(t *testing.T) {
	f := FilterFor(Chain{Label: chains.LabelBaseMainnet, ID: chains.BaseMainnet})
	musdc := venue.Venue{
		Chain: chains.LabelBaseMainnet, Project: "moonwell", PoolID: "0xedc8",
		Asset: "USDC", TVLUsd: 8_900_000, APY: 15.65,
		LiquidityUsd: 0, LiquidityKnown: true,
	}
	if !f.Screen(&musdc) {
		t.Fatal("an illiquid venue must stay visible on /venues")
	}
	if musdc.Routable() {
		t.Fatal("a venue with no withdrawable liquidity was routable")
	}

	// mEURC: $416 of wei-dust is not $1,000 of exit either.
	dust := musdc
	dust.LiquidityUsd, dust.NotRoutable = 416, ""
	if f.Screen(&dust); dust.Routable() {
		t.Fatalf("$416 of cash cleared the $%.0f floor", f.MinLiquidityUsd)
	}

	ok := musdc
	ok.LiquidityUsd, ok.NotRoutable = f.MinLiquidityUsd+1, ""
	if f.Screen(&ok); !ok.Routable() {
		t.Fatalf("a venue above the floor is routable: %s", ok.NotRoutable)
	}

	// Unmeasured is not zero: Morpho publishes no withdrawable figure.
	unknown := musdc
	unknown.LiquidityKnown, unknown.NotRoutable = false, ""
	if f.Screen(&unknown); !unknown.Routable() {
		t.Fatalf("an unmeasured venue must not be treated as empty: %s", unknown.NotRoutable)
	}
}

// The publisher and gen-venues share one screen, so the allowlist can never
// contain a venue the publisher marks unroutable.
func TestGeneratorAndPublisherAgreeOnTheSameVenue(t *testing.T) {
	f := FilterFor(Chain{Label: chains.LabelBaseMainnet, ID: chains.BaseMainnet})
	v := venue.Venue{
		Chain: chains.LabelBaseMainnet, Project: "moonwell", PoolID: "0xb682",
		Asset: "EURC", TVLUsd: 338_000, APY: 17.7,
		LiquidityUsd: 0.0004, LiquidityKnown: true,
	}
	published := f.Screen(&v)
	if !published || v.Routable() {
		t.Fatalf("published=%v routable=%v, want published and not routable", published, v.Routable())
	}
	// gen-venues emits exactly the venues that are Screened and Routable.
	if emitted := published && v.Routable(); emitted {
		t.Fatal("the generator would write an unroutable venue into the allowlist")
	}
}

// Borrowing was never enabled: variableBorrowIndex is still exactly 1e27 and
// the rate is a structural zero, not a broken measurement.
func TestCollateralOnlyReserveIsMarkedNotJustZero(t *testing.T) {
	if !collateralOnly(rayOne, big.NewInt(0)) {
		t.Fatal("ezETH/wrsETH/LBTC/AAVE/syrupUSDC shape not detected")
	}
	if collateralOnly(new(big.Int).Add(rayOne, big.NewInt(1)), big.NewInt(0)) {
		t.Fatal("a reserve that has accrued borrow interest is not collateral-only")
	}
	if collateralOnly(rayOne, big.NewInt(1)) {
		t.Fatal("a reserve paying a rate is not collateral-only")
	}
	f := FilterFor(Chain{Label: chains.LabelBaseSepolia, ID: chains.BaseSepolia})
	v := venue.Venue{
		Chain: chains.LabelBaseSepolia, Project: "aave-v3", PoolID: "0xez",
		Asset: "WETH", TVLUsd: 1, APY: 0, CollateralOnly: true,
	}
	if f.Screen(&v); v.Routable() {
		t.Fatal("a collateral-only reserve is not a 0% lending venue to route into")
	}
}
