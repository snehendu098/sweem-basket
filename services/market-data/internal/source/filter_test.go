package source

import (
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
