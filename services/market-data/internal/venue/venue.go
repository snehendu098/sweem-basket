package venue

import (
	"fmt"
	"strconv"
	"time"
)

type Venue struct {
	ID           string    `json:"id"`
	Chain        string    `json:"chain"`
	Project      string    `json:"project"`
	Symbol       string    `json:"symbol"`
	PoolID       string    `json:"pool_id"`
	Asset        string    `json:"asset"`
	TVLUsd       float64   `json:"tvl_usd"`
	APY          float64   `json:"apy"`
	APYBase      float64   `json:"apy_base"`
	APYReward    float64   `json:"apy_reward"`
	APYIntrinsic float64   `json:"apy_intrinsic"`
	Stablecoin   bool      `json:"stablecoin"`
	UpdatedAt    time.Time `json:"updated_at"`

	// Withdrawable, not supplied: a 100%-utilised market has TVL and no exit.
	// LiquidityKnown false means unmeasured, which is not the same as zero.
	LiquidityUsd   float64 `json:"liquidity_usd"`
	LiquidityKnown bool    `json:"liquidity_known"`
	CollateralOnly bool    `json:"collateral_only,omitempty"`
	NotRoutable    string  `json:"not_routable,omitempty"`
}

func (v Venue) Routable() bool { return v.NotRoutable == "" }

func MakeID(chain, project, poolID string) string {
	return fmt.Sprintf("%s:%s:%s", chain, project, poolID)
}

func (v Venue) Map() map[string]any {
	return map[string]any{
		"id": v.ID, "chain": v.Chain, "project": v.Project, "symbol": v.Symbol,
		"pool_id": v.PoolID, "asset": v.Asset,
		"tvl_usd": f(v.TVLUsd), "apy": f(v.APY), "apy_base": f(v.APYBase), "apy_reward": f(v.APYReward),
		"apy_intrinsic": f(v.APYIntrinsic),
		"stablecoin":    strconv.FormatBool(v.Stablecoin),
		"updated_at":    v.UpdatedAt.UTC().Format(time.RFC3339),
		"liquidity_usd": f(v.LiquidityUsd), "liquidity_known": strconv.FormatBool(v.LiquidityKnown),
		"collateral_only": strconv.FormatBool(v.CollateralOnly),
		"not_routable":    v.NotRoutable,
	}
}

func FromMap(m map[string]string) Venue {
	ts, _ := time.Parse(time.RFC3339, m["updated_at"])
	stable, _ := strconv.ParseBool(m["stablecoin"])
	liqKnown, _ := strconv.ParseBool(m["liquidity_known"])
	collateralOnly, _ := strconv.ParseBool(m["collateral_only"])
	return Venue{
		ID: m["id"], Chain: m["chain"], Project: m["project"], Symbol: m["symbol"],
		PoolID: m["pool_id"], Asset: m["asset"],
		TVLUsd: pf(m["tvl_usd"]), APY: pf(m["apy"]), APYBase: pf(m["apy_base"]), APYReward: pf(m["apy_reward"]),
		APYIntrinsic: pf(m["apy_intrinsic"]),
		Stablecoin:   stable, UpdatedAt: ts,
		LiquidityUsd: pf(m["liquidity_usd"]), LiquidityKnown: liqKnown,
		CollateralOnly: collateralOnly, NotRoutable: m["not_routable"],
	}
}

func f(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func pf(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
