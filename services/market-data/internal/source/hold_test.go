package source

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

// ray is the scale the subgraph normalizes every rate provider onto. Only the
// ratio of two samples is used, so the scale cancels — these helpers just make
// the fixtures readable.
func rayOf(x float64) *big.Int {
	f := new(big.Float).Mul(big.NewFloat(x), new(big.Float).SetFloat64(1e27))
	out, _ := f.Int(nil)
	return out
}

func at(unix int64) time.Time { return time.Unix(unix, 0).UTC() }

// The 30-day window used throughout: real Base block timestamps, 2_592_000s apart.
const (
	tOld = 1786647997
	tNew = 1789239997
)

// TestAnnualizeGrowth pins every rule that decides whether an observed drift
// becomes a rate. The "wstETH 30d" case is the live Base series, hand-computed:
//
//	before 1241589668298845993, after 1243869391109877255 -> growth 1.8362e-3
//	periods = 31536000 / 2592000 = 12.1666667
//	expm1(12.1666667 * log1p(1.8362e-3)) * 100 = 2.2570060%
func TestAnnualizeGrowth(t *testing.T) {
	tests := []struct {
		name      string
		samples   []GrowthSample
		minWindow time.Duration
		maxWindow time.Duration
		want      float64
		wantOK    bool
	}{
		{
			name: "wstETH 30d, live Base exchange rate",
			samples: []GrowthSample{
				{At: at(tNew), Value: big.NewInt(0).SetUint64(1243869391109877255)},
				{At: at(tOld), Value: big.NewInt(0).SetUint64(1241589668298845993)},
			},
			minWindow: 24 * time.Hour, maxWindow: 60 * 24 * time.Hour,
			want: 2.2570060, wantOK: true,
		},
		{
			// 1% over 30 days: expm1(12.1666667*log(1.01))*100 = 12.8695294%
			name: "1% over 30d, hand-computed",
			samples: []GrowthSample{
				{At: at(tOld), Value: rayOf(1.00)},
				{At: at(tNew), Value: rayOf(1.01)},
			},
			minWindow: 24 * time.Hour, maxWindow: 60 * 24 * time.Hour,
			want: 12.8695294, wantOK: true,
		},
		{
			// Five minutes of drift annualizes to a four-digit number. Refuse.
			name: "window too short",
			samples: []GrowthSample{
				{At: at(tNew - 300), Value: rayOf(1.0000)},
				{At: at(tNew), Value: rayOf(1.0001)},
			},
			minWindow: 24 * time.Hour, maxWindow: 60 * 24 * time.Hour,
			wantOK: false,
		},
		{
			// An exchange rate must not fall. Slashing or a broken oracle look
			// identical from here and we route on neither.
			name: "negative growth",
			samples: []GrowthSample{
				{At: at(tOld), Value: rayOf(1.05)},
				{At: at(tNew), Value: rayOf(1.04)},
			},
			minWindow: 24 * time.Hour, maxWindow: 60 * 24 * time.Hour,
			wantOK: false,
		},
		{
			// Flat over a full month is a stale feed, not a 0% asset.
			name: "no growth",
			samples: []GrowthSample{
				{At: at(tOld), Value: rayOf(1.05)},
				{At: at(tNew), Value: rayOf(1.05)},
			},
			minWindow: 24 * time.Hour, maxWindow: 60 * 24 * time.Hour,
			wantOK: false,
		},
		{
			name: "single sample is not a rate",
			samples: []GrowthSample{
				{At: at(tNew), Value: rayOf(1.05)},
			},
			minWindow: 24 * time.Hour, maxWindow: 60 * 24 * time.Hour,
			wantOK: false,
		},
		{
			name: "second sample outside max window",
			samples: []GrowthSample{
				{At: at(tOld), Value: rayOf(1.00)},
				{At: at(tNew), Value: rayOf(1.01)},
			},
			minWindow: 24 * time.Hour, maxWindow: 7 * 24 * time.Hour,
			wantOK: false,
		},
		{
			name:      "no samples at all",
			samples:   nil,
			minWindow: 24 * time.Hour, maxWindow: 60 * 24 * time.Hour,
			wantOK: false,
		},
		{
			name: "unparsed samples are ignored, not treated as zero",
			samples: []GrowthSample{
				{At: at(tOld), Value: nil},
				{At: at(tNew), Value: rayOf(1.01)},
			},
			minWindow: 24 * time.Hour, maxWindow: 60 * 24 * time.Hour,
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := AnnualizeGrowth(tc.name, tc.samples, tc.minWindow, tc.maxWindow)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (apy %v)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				if got != 0 {
					t.Fatalf("rejected series returned apy %v, want 0 alongside ok=false", got)
				}
				return
			}
			close(t, got, tc.want, 1e-6)
		})
	}
}

// TestStackIntrinsic is the reason the rest of the change is visible: a lending
// venue in an asset that also earns on its own must report the SUM, and must
// keep the two components apart.
func TestStackIntrinsic(t *testing.T) {
	in := []venue.Venue{
		{ID: "base:hold:0xwsteth", Project: ProtocolHold, Asset: "wstETH", APY: 3.0, APYIntrinsic: 3.0},
		{ID: "base:aave-v3:0xwsteth", Project: "aave-v3", Asset: "wstETH", APY: 0.1, APYBase: 0.1},
		{ID: "base:morpho-blue:0xvault", Project: "morpho-blue", Asset: "wstETH", APY: 0.4, APYBase: 0.4},
		{ID: "base:aave-v3:0xusdc", Project: "aave-v3", Asset: "USDC", APY: 1.2, APYBase: 1.2},
	}
	got := StackIntrinsic(in)

	tests := []struct {
		id                          string
		wantAPY, wantBase, wantIntr float64
	}{
		// The hold venue already IS the intrinsic rate; stacking must not double it.
		{"base:hold:0xwsteth", 3.0, 0, 3.0},
		// The mispricing this fixes: 0.1% displayed, 3.1% real.
		{"base:aave-v3:0xwsteth", 3.1, 0.1, 3.0},
		{"base:morpho-blue:0xvault", 3.4, 0.4, 3.0},
		// No intrinsic yield on USDC: untouched, and still ranked below wstETH.
		{"base:aave-v3:0xusdc", 1.2, 1.2, 0},
	}
	byID := map[string]venue.Venue{}
	for _, v := range got {
		byID[v.ID] = v
	}
	for _, tc := range tests {
		v, ok := byID[tc.id]
		if !ok {
			t.Fatalf("venue %s missing from result", tc.id)
		}
		close(t, v.APY, tc.wantAPY, 1e-9)
		close(t, v.APYBase, tc.wantBase, 1e-9)
		close(t, v.APYIntrinsic, tc.wantIntr, 1e-9)
	}
	if byID["base:aave-v3:0xwsteth"].APY <= byID["base:aave-v3:0xusdc"].APY {
		t.Fatal("stacked wstETH venue must outrank the USDC venue; this is the whole bug")
	}
}

// An asset with no hold venue must come back byte-identical: stacking may never
// invent a rate for an asset whose intrinsic yield was never measured.
func TestStackIntrinsicNoFeeds(t *testing.T) {
	in := []venue.Venue{
		{ID: "base:aave-v3:0xwsteth", Project: "aave-v3", Asset: "wstETH", APY: 0.1, APYBase: 0.1},
	}
	got := StackIntrinsic(in)
	if got[0].APY != 0.1 || got[0].APYIntrinsic != 0 {
		t.Fatalf("got apy=%v intrinsic=%v, want the input untouched", got[0].APY, got[0].APYIntrinsic)
	}
}

func holdFixture(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/hold_base.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return json.RawMessage(raw)
}

// TestHoldMapFixture pins the drop rules on a 10-day window of live Base
// samples (30 days would exceed DefaultHoldMaxWindow). Four feeds in, one
// venue out:
// cbETH is marked unavailable, weETH has a single snapshot, sUSDS has a real
// rate but no price feed on Base. None of them may appear as a 0% venue.
func TestHoldMapFixture(t *testing.T) {
	h := NewHold("base", "test-id", DefaultHoldMinWindow, DefaultHoldMaxWindow)
	got, err := h.Map(testPricer(), holdFixture(t))
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if len(got) != 1 {
		ids := make([]string, len(got))
		for i, v := range got {
			ids[i] = v.ID
		}
		t.Fatalf("got %d venues %v, want only the wstETH hold venue", len(got), ids)
	}

	v := got[0]
	if v.ID != "base:hold:0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452" {
		t.Errorf("id = %q", v.ID)
	}
	if v.Project != ProtocolHold || v.Asset != "wstETH" {
		t.Errorf("project/asset = %q/%q, want hold/wstETH", v.Project, v.Asset)
	}
	// Hand-computed from the fixture: 1243103211881836544 -> 1243869391109877255
	// over 864000s. periods = 36.5, expm1(36.5*log1p(6.1634e-4))*100 = 2.2744425%.
	close(t, v.APY, 2.2744425, 1e-6)
	close(t, v.APYIntrinsic, 2.2744425, 1e-6)
	if v.APYBase != 0 || v.APYReward != 0 {
		t.Errorf("base/reward = %v/%v, want 0/0: holding pays no lending yield", v.APYBase, v.APYReward)
	}
	// 27219.250972... wstETH at the stub's $3100.
	close(t, v.TVLUsd, 27219.250972291143*3100, 1)
}
