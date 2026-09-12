package source

import (
	"math"
	"testing"

	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

func v(project, poolID string, base, reward, intrinsic float64) venue.Venue {
	return venue.Venue{
		ID:    venue.MakeID("base", project, poolID),
		Chain: "base", Project: project, PoolID: poolID, Asset: "USDC",
		APY: base + reward + intrinsic, APYBase: base, APYReward: reward, APYIntrinsic: intrinsic,
		TVLUsd: 1_000_000,
	}
}

func TestReconcile(t *testing.T) {
	tests := []struct {
		name           string
		subgraph, live []venue.Venue
		wantIDs        []string
		wantAPY        map[string]float64
	}{
		{
			name:     "live rate wins where both have the venue",
			subgraph: []venue.Venue{v("aave-v3", "0xaaa", 1.0, 0, 0)},
			live:     []venue.Venue{v("aave-v3", "0xaaa", 4.0, 0, 0)},
			wantIDs:  []string{"base:aave-v3:0xaaa"},
			wantAPY:  map[string]float64{"base:aave-v3:0xaaa": 4.0},
		},
		{
			// The subgraph is the only source that can measure emissions and
			// LST drift; keeping the live base rate must not throw them away.
			name:     "reward and intrinsic legs survive the merge",
			subgraph: []venue.Venue{v("compound-v3", "0xccc", 1.0, 0.5, 3.0)},
			live:     []venue.Venue{v("compound-v3", "0xccc", 4.0, 0, 0)},
			wantIDs:  []string{"base:compound-v3:0xccc"},
			wantAPY:  map[string]float64{"base:compound-v3:0xccc": 7.5},
		},
		{
			name:     "subgraph-only venues pass through",
			subgraph: []venue.Venue{v("morpho-blue", "0xmmm", 2.0, 0, 0)},
			live:     nil,
			wantIDs:  []string{"base:morpho-blue:0xmmm"},
			wantAPY:  map[string]float64{"base:morpho-blue:0xmmm": 2.0},
		},
		{
			// The usual case while a subgraph is still replaying history.
			name:     "live-only venues pass through",
			subgraph: nil,
			live:     []venue.Venue{v("aave-v3", "0xaaa", 4.0, 0, 0)},
			wantIDs:  []string{"base:aave-v3:0xaaa"},
			wantAPY:  map[string]float64{"base:aave-v3:0xaaa": 4.0},
		},
		{
			// The upstream Aave schema keys a reserve by underlying+provider.
			// It is the same reserve, so it must merge, not double-publish.
			name: "long-form aave subgraph id folds onto the short form",
			subgraph: []venue.Venue{v("aave-v3",
				"0x833589fcd6edb6e08f4c7c32d4f71b54bda02913e20c2c7bb47cbfeff01b1a0ffb6a5a71d3c1ec9b", 1.0, 0.25, 0)},
			live:    []venue.Venue{v("aave-v3", "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913", 4.0, 0, 0)},
			wantIDs: []string{"base:aave-v3:0x833589fcd6edb6e08f4c7c32d4f71b54bda02913"},
			wantAPY: map[string]float64{"base:aave-v3:0x833589fcd6edb6e08f4c7c32d4f71b54bda02913": 4.25},
		},
		{
			name:     "disjoint venues are unioned",
			subgraph: []venue.Venue{v("morpho-blue", "0xmmm", 2.0, 0, 0)},
			live:     []venue.Venue{v("aave-v3", "0xaaa", 4.0, 0, 0)},
			wantIDs:  []string{"base:aave-v3:0xaaa", "base:morpho-blue:0xmmm"},
			wantAPY:  map[string]float64{"base:aave-v3:0xaaa": 4.0, "base:morpho-blue:0xmmm": 2.0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Reconcile(tt.subgraph, tt.live, DefaultRateTolerance)
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("got %d venues, want %d: %v", len(got), len(tt.wantIDs), ids(got))
			}
			for i, want := range tt.wantIDs {
				if got[i].ID != want {
					t.Fatalf("venue %d id = %q, want %q", i, got[i].ID, want)
				}
				if apy := tt.wantAPY[want]; math.Abs(got[i].APY-apy) > 1e-9 {
					t.Errorf("%s APY = %v, want %v", want, got[i].APY, apy)
				}
			}
		})
	}
}

// A live Aave venue with no subgraph twin still has to pick up the staking
// yield of its asset, or a 3.1% wstETH position ranks as a 0.1% one.
func TestReconcileStacksIntrinsicOntoLiveOnlyVenues(t *testing.T) {
	hold := v(ProtocolHold, "0xwsteth", 0, 0, 3.0)
	hold.Asset, hold.APY, hold.APYIntrinsic = "WSTETH", 3.0, 3.0

	live := v("aave-v3", "0xaaa", 0.1, 0, 0)
	live.Asset = "WSTETH"

	got := Reconcile([]venue.Venue{hold}, []venue.Venue{live}, DefaultRateTolerance)
	for _, g := range got {
		if g.Project != "aave-v3" {
			continue
		}
		if math.Abs(g.APY-3.1) > 1e-9 {
			t.Fatalf("stacked APY = %v, want 3.1", g.APY)
		}
		return
	}
	t.Fatal("aave venue missing from the merged set")
}

// StackIntrinsic runs inside GraphSource and again inside Reconcile. Running it
// twice must not add the staking rate twice.
func TestStackIntrinsicIsIdempotent(t *testing.T) {
	hold := v(ProtocolHold, "0xwsteth", 0, 0, 3.0)
	hold.Asset, hold.APY, hold.APYIntrinsic = "WSTETH", 3.0, 3.0
	lend := v("aave-v3", "0xaaa", 0.1, 0, 0)
	lend.Asset = "WSTETH"

	once := StackIntrinsic([]venue.Venue{hold, lend})
	twice := StackIntrinsic(once)
	if math.Abs(twice[1].APY-3.1) > 1e-9 {
		t.Fatalf("APY after two stacks = %v, want 3.1", twice[1].APY)
	}
}

func ids(vs []venue.Venue) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.ID
	}
	return out
}

// The live intrinsic rate comes off the oracle's own round history and is
// available on the first cycle; the subgraph needs a day of samples before it
// can answer at all. Where both have it, the live one wins — and the lending
// venues stacked on top must pick up the live number, not the stale one.
func TestReconcilePrefersLiveIntrinsicRate(t *testing.T) {
	subHold := v(ProtocolHold, "0xwsteth", 0, 0, 2.2)
	subHold.Asset, subHold.APY = "WSTETH", 2.2
	subLend := v("aave-v3", "0xaaa", 0.1, 0, 2.2)
	subLend.Asset, subLend.APY = "WSTETH", 2.3 // already stacked by GraphSource

	liveHold := v(ProtocolHold, "0xwsteth", 0, 0, 3.5)
	liveHold.Asset, liveHold.APY = "WSTETH", 3.5
	liveLend := v("aave-v3", "0xaaa", 0.1, 0, 0)
	liveLend.Asset = "WSTETH"

	got := Reconcile([]venue.Venue{subHold, subLend}, []venue.Venue{liveHold, liveLend}, DefaultRateTolerance)
	if len(got) != 2 {
		t.Fatalf("got %d venues, want 2: %v", len(got), ids(got))
	}
	for _, g := range got {
		var want float64
		switch g.Project {
		case ProtocolHold:
			want = 3.5
		default:
			want = 3.6 // 0.1 lending + the live 3.5 staking rate
		}
		if math.Abs(g.APY-want) > 1e-9 {
			t.Errorf("%s APY = %v, want %v", g.ID, g.APY, want)
		}
		if math.Abs(g.APYIntrinsic-3.5) > 1e-9 {
			t.Errorf("%s intrinsic = %v, want the live 3.5", g.ID, g.APYIntrinsic)
		}
	}
}
