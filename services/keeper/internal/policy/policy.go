package policy

import "time"

const yearHours = 365 * 24 * time.Hour

type Params struct {
	MinDriftAPY    float64
	MinPositionUSD float64
	MinHold        time.Duration
	RewardDiscount float64
	GasCostUSD     float64
	SafetyMargin   float64
	MaxPerDay      int
	Horizon        time.Duration
}

const (
	ReasonWorthIt      = "worth_it"
	ReasonPositionTiny = "position_below_floor"
	ReasonRateLimited  = "rate_limited"
	ReasonHoldPeriod   = "within_hold_period"
	ReasonDriftFloor   = "drift_below_floor"
	ReasonNotWorthGas  = "gain_below_breakeven"
	ReasonSameVenue    = "already_in_best_venue"
)

type Input struct {
	Asset        string
	PositionUSD  float64
	CurrentAPY   float64
	BestAPY      float64
	SameVenue    bool
	LastMove     time.Time
	MovesLast24h int
	Now          time.Time
}

type Decision struct {
	Move    bool
	Reason  string
	Drift   float64
	GainUSD float64
	CostUSD float64
}

func EffectiveAPY(base, reward, intrinsic, discount float64) float64 {
	return base + reward*discount + intrinsic
}

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
	case !in.LastMove.IsZero() && in.Now.Sub(in.LastMove) < p.MinHold:
		d.Reason = ReasonHoldPeriod
		return d
	case d.Drift < p.MinDriftAPY:
		d.Reason = ReasonDriftFloor
		return d
	}

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
