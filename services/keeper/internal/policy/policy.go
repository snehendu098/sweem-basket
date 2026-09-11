// Package policy decides whether moving a position is worth what it costs.
// Everything here is pure: no clock, no network, no DB. That is deliberate —
// this is the only code in the keeper that can quietly lose a user money, so
// it must be exhaustively testable in isolation.
package policy

import "time"

const yearHours = 365 * 24 * time.Hour

// Params are the operator-tunable guards. APY values are percentage points
// (5.0 means 5% per year), USD values are dollars.
type Params struct {
	MinDriftAPY    float64       // absolute floor on drift, percentage points
	MinPositionUSD float64       // below this, gas dominates
	MinHold        time.Duration // hysteresis window per user+asset
	RewardDiscount float64       // 0..1 weight applied to reward APY
	GasCostUSD     float64       // withdraw + approve + deposit, one leg
	SafetyMargin   float64       // gain must beat cost by this multiple
	MaxPerDay      int           // rebalance legs per user per 24h
	// Horizon is how long we assume the new position is held when pricing the
	// move. Defaults to MinHold, which is the strictest reading: only pay gas
	// if the gain is recovered before we would even be allowed to move again.
	// Set it longer (e.g. 720h) if you believe positions actually sit longer.
	Horizon time.Duration
}

// Reasons. Every decision carries one, including the skips — "did nothing"
// must be as auditable as "moved funds".
const (
	ReasonWorthIt      = "worth_it"
	ReasonPositionTiny = "position_below_floor"
	ReasonRateLimited  = "rate_limited"
	ReasonHoldPeriod   = "within_hold_period"
	ReasonDriftFloor   = "drift_below_floor"
	ReasonNotWorthGas  = "gain_below_breakeven"
	ReasonSameVenue    = "already_in_best_venue"
)

// Input is one position measured against the best venue available now.
type Input struct {
	Asset        string
	PositionUSD  float64
	CurrentAPY   float64   // effective APY where the money sits, percentage points
	BestAPY      float64   // effective APY of the candidate venue
	SameVenue    bool      // candidate is where the money already is
	LastMove     time.Time // last successful rebalance for this user+asset; zero = never
	MovesLast24h int
	Now          time.Time
}

// Decision is the verdict plus the numbers behind it, for the audit log.
type Decision struct {
	Move    bool
	Reason  string
	Drift   float64 // BestAPY - CurrentAPY, percentage points
	GainUSD float64 // extra yield earned over one MinHold period
	CostUSD float64 // gas for the move, scaled by SafetyMargin
}

// EffectiveAPY discounts reward APY: emissions can stop tomorrow, base yield
// cannot. discount of 0.5 says a reward point is worth half a base point.
func EffectiveAPY(base, reward, discount float64) float64 {
	return base + reward*discount
}

// Evaluate applies the guards in cheapest-first order and returns the verdict.
func Evaluate(in Input, p Params) Decision {
	d := Decision{
		Drift:   in.BestAPY - in.CurrentAPY,
		CostUSD: p.GasCostUSD * p.SafetyMargin,
	}

	switch {
	case in.SameVenue:
		d.Reason = ReasonSameVenue
		return d
	case in.PositionUSD < p.MinPositionUSD:
		d.Reason = ReasonPositionTiny
		return d
	case p.MaxPerDay > 0 && in.MovesLast24h >= p.MaxPerDay:
		d.Reason = ReasonRateLimited
		return d
	// Hysteresis. Two venues within noise of each other will otherwise
	// ping-pong the funds until gas eats the position.
	case !in.LastMove.IsZero() && in.Now.Sub(in.LastMove) < p.MinHold:
		d.Reason = ReasonHoldPeriod
		return d
	case d.Drift < p.MinDriftAPY:
		d.Reason = ReasonDriftFloor
		return d
	}

	// Breakeven: the extra yield earned over one hold period must beat the
	// gas of moving, with margin. Anything less is churn.
	horizon := p.Horizon
	if horizon <= 0 {
		horizon = p.MinHold
	}
	gainPerYear := in.PositionUSD * d.Drift / 100
	d.GainUSD = gainPerYear * (float64(horizon) / float64(yearHours))
	if d.GainUSD <= d.CostUSD {
		d.Reason = ReasonNotWorthGas
		return d
	}

	d.Move, d.Reason = true, ReasonWorthIt
	return d
}
