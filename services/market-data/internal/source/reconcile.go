package source

import (
	"log/slog"
	"math"
	"sort"
	"strings"

	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

// DefaultRateTolerance is how far, in percentage points of APY, the two sources
// may differ before we say so. It is a signal, not an error: a gap means either
// the subgraph is lagging its sync or one of the unit conversions is wrong, and
// it is how a repeat of the per-block/per-second mistake gets caught within one
// cycle instead of after a rebalance.
const DefaultRateTolerance = 0.25

// Reconcile merges the subgraph view with the live on-chain view.
//
// Each APY component has exactly one source that can actually measure it, and
// the merge says so rather than picking a winner wholesale:
//
//	APYBase       rpc        the rate the contract is paying right now
//	APYReward     subgraph   emissions need a token price the chain will not give us
//	APYIntrinsic  rpc, else subgraph
//
// The intrinsic leg moved to the live source once the exchange-rate oracles'
// own round history turned out to be readable on chain: the subgraph derives
// the same number, but only after a day of samples. Where the live source has
// nothing the subgraph value is still used.
//
// So a venue both sources produce keeps the live base rate — the subgraph may
// be days behind — without losing the two numbers the subgraph is the only
// source for. Dropping them would turn a 3.1% wstETH position into a 0.1% one,
// which is precisely the misranking StackIntrinsic exists to prevent.
//
// Venues only one source produced pass through untouched.
func Reconcile(subgraph, live []venue.Venue, tolerance float64) []venue.Venue {
	if tolerance <= 0 {
		tolerance = DefaultRateTolerance
	}
	bySub := make(map[string]venue.Venue, len(subgraph))
	for _, v := range subgraph {
		bySub[reconcileKey(v)] = v
	}

	out := make([]venue.Venue, 0, len(subgraph)+len(live))
	merged := map[string]bool{}
	for _, lv := range live {
		key := reconcileKey(lv)
		sv, both := bySub[key]
		if !both {
			out = append(out, lv)
			continue
		}
		merged[key] = true
		if diff := math.Abs(lv.APYBase - sv.APYBase); diff > tolerance {
			slog.Warn("rate sources disagree, using the live one",
				"venue", lv.ID, "chain", lv.Chain, "project", lv.Project,
				"rpc_apy_base", lv.APYBase, "subgraph_apy_base", sv.APYBase,
				"diff_pp", diff, "tolerance_pp", tolerance)
		}
		// Take each leg from whichever source could actually measure it. A zero
		// on the live side means "not readable on chain", not "zero" — Comet's
		// reward APR needs a COMP price, and a lending venue's intrinsic rate
		// belongs to the asset, not the market. Where the live source DOES
		// measure a leg (a hold venue's intrinsic rate, read straight off the
		// exchange-rate oracle), it wins, because the subgraph needs a day of
		// history before it can answer at all.
		if lv.APYReward == 0 {
			lv.APYReward = sv.APYReward
		}
		if lv.APYIntrinsic == 0 {
			lv.APYIntrinsic = sv.APYIntrinsic
		}
		lv.APY = lv.APYBase + lv.APYReward + lv.APYIntrinsic
		out = append(out, lv)
	}
	for _, sv := range subgraph {
		if !merged[reconcileKey(sv)] {
			out = append(out, sv)
		}
	}

	// A live venue with no subgraph twin (the usual case while a subgraph is
	// still syncing) has never been through intrinsic stacking. StackIntrinsic
	// skips anything already stacked, so running it here covers those without
	// double-counting the ones GraphSource already handled.
	out = StackIntrinsic(out)

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// reconcileKey is the venue id, except that an Aave reserve can arrive under
// two id shapes and they are the same reserve.
//
// The upstream Aave subgraph keys a reserve by `underlying + addressesProvider`
// (80 hex chars); ours, and the executor allowlist, key it by the underlying
// alone (40). Matching on the raw id would publish both as separate venues and
// only one of them would be routable, so the long form is folded onto the short
// one HERE — the emitted id is never rewritten, only the match.
func reconcileKey(v venue.Venue) string {
	if v.Project == "aave-v3" {
		if raw := strings.TrimPrefix(strings.ToLower(v.PoolID), "0x"); len(raw) == 80 {
			return venue.MakeID(v.Chain, v.Project, "0x"+raw[:40])
		}
	}
	return v.ID
}
