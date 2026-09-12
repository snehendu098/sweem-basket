package source

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"testing"
	"time"
)

func morphoFixture(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/morpho.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return json.RawMessage(raw)
}

func newMorphoTest() *MorphoBlue {
	return NewMorphoBlue("base", "test-id", DefaultMorphoMinWindow, DefaultMorphoMaxWindow)
}

// TestMorphoMapFixture pins the whole mapping: the vaults with a wide, positive,
// priceable window survive — including the non-pegged mwETH vault, which used to
// be dropped for want of a price and is now valued from Chainlink.
func TestMorphoMapFixture(t *testing.T) {
	got, err := newMorphoTest().Map(testPricer(), morphoFixture(t))
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if len(got) != 2 {
		ids := make([]string, len(got))
		for i, v := range got {
			ids[i] = v.ID
		}
		t.Fatalf("want 2 venues, got %d: %v", len(got), ids)
	}
	v, weth := got[0], got[1]
	// 9400 raw/1e18 = 9400 WETH at the stub's $2500, and not flagged pegged.
	if weth.Asset != "WETH" || weth.Stablecoin {
		t.Errorf("weth vault asset/stable = %q/%v", weth.Asset, weth.Stablecoin)
	}
	close(t, weth.TVLUsd, 23_500_000, 1)
	// Venue id must match executor/venues.json byte for byte, lowercase hex.
	if want := "base:morpho-blue:0xbeef010f9cb27031ad51e3333f9af9c6b1228183"; v.ID != want {
		t.Errorf("id = %q, want %q", v.ID, want)
	}
	if v.Symbol != "steakUSDC" || v.Asset != "USDC" || !v.Stablecoin {
		t.Errorf("symbol/asset/stable = %q/%q/%v", v.Symbol, v.Asset, v.Stablecoin)
	}
	// 24_800_000_000_000 raw / 10^6 = 24.8M USDC, priced at $1.
	close(t, v.TVLUsd, 24_800_000, 1)
	close(t, v.APY, 5.0, 1e-6)
	if v.APYBase != v.APY {
		t.Errorf("apy_base %v != apy %v", v.APYBase, v.APY)
	}
	// MORPHO emissions come from an off-vault URD: structurally 0 here.
	if v.APYReward != 0 {
		t.Errorf("apy_reward = %v, want 0", v.APYReward)
	}
	f := Filter{Chains: []string{"Base"}, MinTVLUsd: 100_000, MaxAPY: 200}
	if !f.Accept(v) {
		t.Errorf("venue rejected by the standard filter: %+v", v)
	}
}

// TestMorphoVenueIDsMatchExecutorAllowlist checks the exact strings the
// executor allowlist carries; a casing drift here silently unroutes the venue.
func TestMorphoVenueIDsMatchExecutorAllowlist(t *testing.T) {
	m := newMorphoTest()
	allowlist := []string{
		"base:morpho-blue:0xbeef010f9cb27031ad51e3333f9af9c6b1228183",
		"base:morpho-blue:0xee8f4ec5672f09119b96ab6fb59c27e1b7e44b61",
		"base:morpho-blue:0xc1256ae5ff1cf2719d4937adb3bbccab2e00a2ca",
	}
	// Checksummed input from the subgraph would still have to normalize down.
	inputs := []string{
		"0xbeeF010f9cb27031ad51e3333f9aF9C6B1228183",
		"0xee8f4ec5672f09119b96ab6fb59c27e1b7e44b61",
		"0xc1256Ae5FF1cf2719D4937adb3bbCCab2E00A2Ca",
	}
	raw, err := json.Marshal(map[string]any{"vaults": morphoVaultsJSON(inputs)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Map(testPricer(), raw)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if len(got) != len(allowlist) {
		t.Fatalf("want %d venues, got %d", len(allowlist), len(got))
	}
	for i, want := range allowlist {
		if got[i].ID != want {
			t.Errorf("id[%d] = %q, want %q", i, got[i].ID, want)
		}
	}
}

// morphoVaultsJSON builds minimal healthy USDC vaults (24h window, +5% APY).
func morphoVaultsJSON(ids []string) []map[string]any {
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{
			"id": id, "name": "v", "symbol": "vUSDC",
			"asset":         "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913",
			"assetSymbol":   "USDC",
			"assetDecimals": 6,
			"totalAssets":   "5000000000000",
			"totalSupply":   "4900000000000000000000000",
			"snapshots": []map[string]any{
				{"timestamp": "1704067200", "sharePriceScaled": "1020136354229455709157519", "totalAssets": "5000000000000"},
				{"timestamp": "1703980800", "sharePriceScaled": "1020000000000000000000000", "totalAssets": "4990000000000"},
			},
		})
	}
	return out
}

// TestMorphoSharePriceGrowthAPY hand-computes the rate the adapter derives.
//
// Units: sharePriceScaled = totalAssets(6dp) * 1e36 / totalSupply(18dp), so a
// USDC vault sits near 1.02e24. The 1e36 cancels in the ratio; only growth and
// elapsed matter.
func TestMorphoSharePriceGrowthAPY(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		name     string
		before   string
		after    string
		elapsed  time.Duration
		wantAPY  float64
		tolerate float64
	}{
		{
			// 1.020000e24 -> 1.020136354229455709157519e24 over 24h.
			// growth = 1.0001336819..., (growth)^365 - 1 = 0.05 -> 5.000000%
			name:   "1.02e24 scaled USDC share price, +0.01336819% over 24h = 5% APY",
			before: "1020000000000000000000000", after: "1020136354229455709157519",
			elapsed: day, wantAPY: 5.0, tolerate: 1e-6,
		},
		{
			// 1e24 -> 1e24 * 1.0001 over 12h: (1.0001)^730 - 1 = 7.5722%
			name:   "1e24 scaled, +0.01% over 12h = 7.572661% APY",
			before: "1000000000000000000000000", after: "1000100000000000000000000",
			elapsed: 12 * time.Hour, wantAPY: 7.572661, tolerate: 1e-5,
		},
		{
			name:   "flat share price = 0% APY",
			before: "1020000000000000000000000", after: "1020000000000000000000000",
			elapsed: day, wantAPY: 0, tolerate: 0,
		},
		{
			name:   "falling share price yields no rate at all",
			before: "1010000000000000000000000", after: "1009000000000000000000000",
			elapsed: day, wantAPY: 0, tolerate: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SharePriceGrowthToAPY(bi(t, tc.before), bi(t, tc.after), tc.elapsed)
			close(t, got, tc.wantAPY, tc.tolerate)
		})
	}
}

// TestMorphoBigIntPrecision: a 1e36-scaled pair whose difference is smaller than
// one float64 ulp at that magnitude. Parsing the operands into float64 first
// collapses them to the same value and reports 0% APY; dividing in math/big
// keeps the growth. This is the silent-wrong-APY case the big path exists for.
func TestMorphoBigIntPrecision_1e36PairBelowFloat64ULP(t *testing.T) {
	const (
		beforeS = "1000000000000000000000000000000000000" // 1e36
		afterS  = "1000000000000000116200000000000000000" // 1e36 + 1.162e20
	)
	bf, _ := strconv.ParseFloat(beforeS, 64)
	af, _ := strconv.ParseFloat(afterS, 64)
	if bf != af {
		t.Fatalf("fixture no longer collapses in float64: %v vs %v", bf, af)
	}
	naive := math.Expm1((365*24/6)*math.Log1p(af/bf-1)) * 100
	if naive != 0 {
		t.Fatalf("naive float64 path unexpectedly non-zero: %v", naive)
	}
	got := SharePriceGrowthToAPY(bi(t, beforeS), bi(t, afterS), 6*time.Hour)
	if got <= 0 {
		t.Fatalf("big.Int path lost the growth too: got %v", got)
	}
}

// TestMorphoDropRules covers every reason a vault must not be published.
func TestMorphoDropRules(t *testing.T) {
	const (
		t0    = "1704067200" // 2024-01-01T00:00:00Z
		t12h  = "1704024000"
		t24h  = "1703980800"
		t20m  = "1704066000"
		t8d   = "1703376000"
		pLow  = "1020000000000000000000000"
		pHigh = "1020136354229455709157519"
	)
	snap := func(ts, price string) map[string]any {
		return map[string]any{"timestamp": ts, "sharePriceScaled": price, "totalAssets": "5000000000000"}
	}
	vault := func(supply string, snaps ...map[string]any) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"vaults": []map[string]any{{
			"id": "0xbeef010f9cb27031ad51e3333f9af9c6b1228183", "symbol": "steakUSDC",
			"asset":         "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913",
			"assetSymbol":   "USDC",
			"assetDecimals": 6,
			"totalAssets":   "5000000000000",
			"totalSupply":   supply,
			"snapshots":     snaps,
		}}})
		return raw
	}
	good := "4900000000000000000000000"

	tests := []struct {
		name string
		raw  json.RawMessage
		want int
	}{
		{"24h window, positive growth: kept", vault(good, snap(t0, pHigh), snap(t24h, pLow)), 1},
		{"single snapshot: no rate, dropped", vault(good, snap(t0, pHigh)), 0},
		{"no snapshots: dropped", vault(good), 0},
		{"20 minute window below MORPHO_MIN_WINDOW 6h: dropped", vault(good, snap(t0, pHigh), snap(t20m, pLow)), 0},
		{"negative growth: dropped", vault(good, snap(t0, pLow), snap(t24h, pHigh)), 0},
		{"zero totalSupply never reaches a division: dropped", vault("0", snap(t0, pHigh), snap(t24h, pLow)), 0},
		{"unparseable snapshot leaves one usable point: dropped",
			vault(good, snap(t0, pHigh), snap(t24h, "not-a-number")), 0},
		{"only sample older than MORPHO_MAX_WINDOW 7d: dropped", vault(good, snap(t0, pHigh), snap(t8d, pLow)), 0},
		{"12h sample present alongside an out-of-range one: kept via 12h",
			vault(good, snap(t0, pHigh), snap(t12h, pLow), snap(t8d, "1000000000000000000000000")), 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newMorphoTest().Map(testPricer(), tc.raw)
			if err != nil {
				t.Fatalf("map: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("got %d venues, want %d (%+v)", len(got), tc.want, got)
			}
		})
	}
}

// TestMorphoWidestWindowWins: with 6h and 24h samples both available the
// adapter must use the 24h one — the shorter window is noisier.
func TestMorphoWidestWindowWins(t *testing.T) {
	m := newMorphoTest()
	snaps := []morphoSnapshot{
		{Timestamp: "1704067200", SharePriceScaled: "1020136354229455709157519"},
		{Timestamp: "1704045600", SharePriceScaled: "1020100000000000000000000"}, // -6h
		{Timestamp: "1703980800", SharePriceScaled: "1020000000000000000000000"}, // -24h
	}
	got, ok := m.vaultAPY("v", snaps)
	if !ok {
		t.Fatal("expected a rate")
	}
	close(t, got, SharePriceGrowthToAPY(bi(t, "1020000000000000000000000"), bi(t, "1020136354229455709157519"), 24*time.Hour), 1e-9)
}

// TestMorphoUnconfiguredSkipsCleanly: no MORPHO_SUBGRAPH_ID means the adapter
// still exists (so /sources reports it) but carries no id to query.
func TestMorphoUnconfiguredSkipsCleanly(t *testing.T) {
	t.Setenv("MORPHO_SUBGRAPH_ID_8453", "")
	m := NewMorphoBlue("base", "", 0, 0)
	if m.SubgraphID() != "" {
		t.Errorf("subgraph id = %q, want empty", m.SubgraphID())
	}
	if m.Protocol() != "morpho-blue" {
		t.Errorf("protocol = %q", m.Protocol())
	}
	if m.MinWindow != DefaultMorphoMinWindow || m.MaxWindow != DefaultMorphoMaxWindow {
		t.Errorf("windows = %v/%v, want defaults", m.MinWindow, m.MaxWindow)
	}
	found := false
	for _, a := range Adapters(Chain{Label: "base", ID: 8453}) {
		if a.Protocol() == "morpho-blue" {
			found = true
		}
	}
	if !found {
		t.Error("unconfigured morpho adapter must still be listed for /sources")
	}
}
