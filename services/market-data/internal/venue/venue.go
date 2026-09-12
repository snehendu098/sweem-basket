// Package venue holds the normalized yield-venue model shared by publisher and API.
package venue

import (
	"fmt"
	"strconv"
	"time"
)

// Venue is one place a single asset can earn yield.
//
// The three APY components are kept apart because they are different in kind
// and a consumer needs to tell them apart: APYBase is paid by borrower demand
// and can be gone tomorrow, APYReward is an emissions programme with an end
// date, APYIntrinsic is the asset appreciating on its own and is the stickiest
// of the three. APY is their sum and is the ONLY field to rank on — a wstETH
// position earning 0.1% lending on top of 3.0% staking is a 3.1% position, and
// ranking on APYBase is how the keeper "improves" a user out of it.
type Venue struct {
	ID        string  `json:"id"`
	Chain     string  `json:"chain"`
	Project   string  `json:"project"`
	Symbol    string  `json:"symbol"`
	PoolID    string  `json:"pool_id"`
	Asset     string  `json:"asset"`
	TVLUsd    float64 `json:"tvl_usd"`
	APY       float64 `json:"apy"`
	APYBase   float64 `json:"apy_base"`
	APYReward float64 `json:"apy_reward"`
	// APYIntrinsic is yield the asset earns with no protocol interaction at
	// all — a liquid staking token's exchange rate, a savings-rate stablecoin's
	// chi. Zero means "none measured": an asset with no rate feed and an asset
	// whose feed reverted both land here, and neither is a claim of 0% yield.
	APYIntrinsic float64   `json:"apy_intrinsic"`
	Stablecoin   bool      `json:"stablecoin"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// MakeID builds the stable venue id.
func MakeID(chain, project, poolID string) string {
	return fmt.Sprintf("%s:%s:%s", chain, project, poolID)
}

// Map renders the venue as a Redis hash.
func (v Venue) Map() map[string]any {
	return map[string]any{
		"id": v.ID, "chain": v.Chain, "project": v.Project, "symbol": v.Symbol,
		"pool_id": v.PoolID, "asset": v.Asset,
		"tvl_usd": f(v.TVLUsd), "apy": f(v.APY), "apy_base": f(v.APYBase), "apy_reward": f(v.APYReward),
		"apy_intrinsic": f(v.APYIntrinsic),
		"stablecoin":    strconv.FormatBool(v.Stablecoin),
		"updated_at":    v.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// FromMap parses a Redis hash back into a Venue.
func FromMap(m map[string]string) Venue {
	ts, _ := time.Parse(time.RFC3339, m["updated_at"])
	stable, _ := strconv.ParseBool(m["stablecoin"])
	return Venue{
		ID: m["id"], Chain: m["chain"], Project: m["project"], Symbol: m["symbol"],
		PoolID: m["pool_id"], Asset: m["asset"],
		TVLUsd: pf(m["tvl_usd"]), APY: pf(m["apy"]), APYBase: pf(m["apy_base"]), APYReward: pf(m["apy_reward"]),
		APYIntrinsic: pf(m["apy_intrinsic"]),
		Stablecoin:   stable, UpdatedAt: ts,
	}
}

func f(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func pf(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
