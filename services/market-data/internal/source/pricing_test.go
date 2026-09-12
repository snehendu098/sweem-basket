package source

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/prices"
)

// stubFeed prices from a fixed table. A symbol in stale answers ErrStale, so
// the "feed exists but the round is old" path is exercised without a network.
type stubFeed struct {
	usd   map[string]float64
	stale map[string]bool
}

func (s stubFeed) USD(_ context.Context, symbol string) (prices.Price, error) {
	sym := strings.ToUpper(symbol)
	if s.stale[sym] {
		return prices.Price{}, prices.ErrStale
	}
	v, ok := s.usd[sym]
	if !ok {
		return prices.Price{}, prices.ErrNoFeed
	}
	return prices.Price{USD: v, UpdatedAt: time.Now()}, nil
}

// testPricer is the default price table for mapper tests.
func testPricer() *Pricer {
	return NewPricer(context.Background(), stubFeed{usd: map[string]float64{
		"USDC": 1, "DAI": 1, "WETH": 2500, "ETH": 2500, "CBBTC": 78000, "WSTETH": 3100,
	}})
}

// A missing feed, a stale round and a zero answer must all be reported as
// unpriceable. None of them may fall through to a default value.
func TestPricerNeverDefaults(t *testing.T) {
	feed := stubFeed{
		usd:   map[string]float64{"USDC": 0.9999, "WETH": 2500, "GHO": 0},
		stale: map[string]bool{"DAI": true},
	}
	tests := []struct {
		name, asset string
		want        float64
		wantOK      bool
	}{
		{"priced", "USDC", 0.9999, true},
		{"priced, case-insensitive", "weth", 2500, true},
		{"no feed", "LBTC", 0, false},
		{"stale round is not a dollar", "DAI", 0, false},
		{"non-positive answer", "GHO", 0, false},
		{"unresolved asset", "", 0, false},
	}
	p := NewPricer(context.Background(), feed)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := p.USD(tc.asset)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("USD(%q) = %v, %v; want %v, %v", tc.asset, got, ok, tc.want, tc.wantOK)
			}
		})
	}

	// Everything dropped must be visible on /sources, not silently absent.
	want := map[string]bool{"LBTC": true, "DAI": true, "GHO": true}
	got := p.Unpriceable()
	if len(got) != len(want) {
		t.Fatalf("unpriceable = %+v, want %d entries", got, len(want))
	}
	for _, u := range got {
		if !want[u.Asset] {
			t.Errorf("unexpected unpriceable asset %q", u.Asset)
		}
		if u.Reason == "" {
			t.Errorf("%s: reason must say why", u.Asset)
		}
	}
}

// A nil feed must drop venues rather than value them at anything.
func TestPricerWithoutFeedDropsEverything(t *testing.T) {
	p := NewPricer(context.Background(), nil)
	if v, ok := p.USD("USDC"); ok || v != 0 {
		t.Fatalf("got %v, %v; want 0, false", v, ok)
	}
}

// Morpho carries no USD field at all: TVL is units x Chainlink price, and a
// vault whose asset has no usable price is dropped, never valued at $1.
func TestMorphoPricesFromFeed(t *testing.T) {
	vault := func(symbol, asset, assetSymbol string, decimals int, totalAssets string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"vaults": []map[string]any{{
			"id": "0xa0e430870c4604ccfc7b38ca7845b1ff653d0ff1", "symbol": symbol,
			"asset": asset, "assetSymbol": assetSymbol, "assetDecimals": decimals,
			"totalAssets": totalAssets,
			"totalSupply": "9300000000000000000000",
			"snapshots": []map[string]any{
				{"timestamp": "1704067200", "sharePriceScaled": "1010752688172043010752688"},
				{"timestamp": "1703980800", "sharePriceScaled": "1010600000000000000000000"},
			},
		}}})
		return raw
	}
	const weth = "0x4200000000000000000000000000000000000006"
	const lbtc = "0xecac9c5f704e954931349da37f60e39f515c11c1"

	tests := []struct {
		name    string
		raw     json.RawMessage
		wantTVL float64 // 0 means "vault must be dropped"
	}{
		// 9400 WETH x $2500. Before Chainlink this vault was dropped for not
		// being a stablecoin.
		{"non-pegged vault priced", vault("mwETH", weth, "WETH", 18, "9400000000000000000000"), 23_500_000},
		// LBTC has no verified aggregator on Base: no price, no venue.
		{"asset without a feed dropped", vault("mwLBTC", lbtc, "LBTC", 8, "100000000000"), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newMorphoTest().Map(testPricer(), tc.raw)
			if err != nil {
				t.Fatalf("map: %v", err)
			}
			if tc.wantTVL == 0 {
				if len(got) != 0 {
					t.Fatalf("got %+v, want the vault dropped", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %d venues, want 1", len(got))
			}
			close(t, got[0].TVLUsd, tc.wantTVL, 1)
			if got[0].Stablecoin {
				t.Error("WETH vault flagged as a stablecoin")
			}
		})
	}
}

// Several live Aave Base reserves report priceInEth = 0. They must be valued
// from Chainlink, and dropped only if Chainlink cannot price them either.
func TestAavePricesZeroOracleFromFeed(t *testing.T) {
	reserve := func(symbol, asset, priceInEth string, decimals int, liquidity string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"reserves": []map[string]any{{
			"id": "0xres", "symbol": symbol, "decimals": decimals,
			"underlyingAsset": asset,
			"liquidityRate":   "30000000000000000000000000", // 3% ray
			"totalLiquidity":  liquidity,
			"isActive":        true, "isFrozen": false, "isPaused": false,
			"price": map[string]any{
				"priceInEth": priceInEth,
				"oracle":     map[string]any{"baseCurrencyUnit": "100000000"},
			},
		}}})
		return raw
	}
	const wsteth = "0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452"
	const gho = "0x6bb7a212910682dcfdbd5bcbb3e28fb4e8da10ee"

	tests := []struct {
		name    string
		raw     json.RawMessage
		wantTVL float64 // 0 means "reserve must be dropped"
	}{
		// Subgraph price wins when it has one: 1000 USDC-scale units x $1.
		{"oracle price used", reserve("WSTETH", wsteth, "310000000000", 18, "1000000000000000000000"), 3_100_000},
		// priceInEth = 0: fall back to Chainlink, 1000 wstETH x $3100.
		{"zero oracle price falls back to chainlink", reserve("WSTETH", wsteth, "0", 18, "1000000000000000000000"), 3_100_000},
		// No feed for GHO in this stub: dropped, not valued at its peg.
		{"unpriceable reserve dropped", reserve("GHO", gho, "0", 18, "1000000000000000000000"), 0},
	}
	a := NewAaveV3("base", "test-id")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := a.Map(testPricer(), tc.raw)
			if err != nil {
				t.Fatalf("map: %v", err)
			}
			if tc.wantTVL == 0 {
				if len(got) != 0 {
					t.Fatalf("got %+v, want the reserve dropped", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %d venues, want 1", len(got))
			}
			close(t, got[0].TVLUsd, tc.wantTVL, 1)
		})
	}
}

// A stale feed must drop the venue, never publish a defaulted TVL.
func TestStalePriceDropsVenue(t *testing.T) {
	p := NewPricer(context.Background(), stubFeed{
		usd:   map[string]float64{"USDC": 1},
		stale: map[string]bool{"USDC": true},
	})
	got, err := newMorphoTest().Map(p, morphoFixture(t))
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	for _, v := range got {
		if v.Asset == "USDC" {
			t.Fatalf("stale USDC feed still produced a venue: %+v", v)
		}
	}
	if u := p.Unpriceable(); len(u) == 0 {
		t.Error("stale asset must be reported on /sources")
	}
}
