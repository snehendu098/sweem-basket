package source

import (
	"log/slog"
	"math"
	"sort"
	"strings"

	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

const DefaultRateTolerance = 0.25

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

	out = StackIntrinsic(out)

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func reconcileKey(v venue.Venue) string {
	if v.Project == "aave-v3" {
		if raw := strings.TrimPrefix(strings.ToLower(v.PoolID), "0x"); len(raw) == 80 {
			return venue.MakeID(v.Chain, v.Project, "0x"+raw[:40])
		}
	}
	return v.ID
}
