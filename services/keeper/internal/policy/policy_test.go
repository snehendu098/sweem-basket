package policy

import (
	"math"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

func base() Params {
	return Params{
		MinDriftAPY:    0.5,
		MinPositionUSD: 50,
		MinHold:        6 * time.Hour,
		RewardDiscount: 0.5,
		GasCostUSD:     2,
		SafetyMargin:   1.5,
		MaxPerDay:      4,
		Horizon:        365 * 24 * time.Hour, // price the move over a year
	}
}

func TestEffectiveAPY(t *testing.T) {
	tests := []struct {
		name                              string
		base, reward, intrinsic, discount float64
		want                              float64
	}{
		{"pure base is untouched", 5, 0, 0, 0.5, 5},
		{"reward halved at 0.5", 4, 6, 0, 0.5, 7},
		{"reward ignored at 0", 4, 6, 0, 0, 4},
		{"reward trusted at 1", 4, 6, 0, 1, 10},
		// Moonwell's subgraph folds emissions into the base rate, so a venue
		// can report a big apy_base with apy_reward=0 and dodge the discount.
		{"undisclosed rewards are not discountable", 14.5, 0, 0, 0.5, 14.5},
		// Staking yield is issuance, not an incentive programme: it counts in
		// full at every discount.
		{"intrinsic is undiscounted", 0.85, 0, 2.26, 0.5, 3.11},
		{"intrinsic survives a zero discount", 0.85, 6, 2.26, 0, 3.11},
		{"a hold venue is all intrinsic", 0, 0, 2.26, 0.5, 2.26},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveAPY(tt.base, tt.reward, tt.intrinsic, tt.discount); math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestEvaluateBreakeven(t *testing.T) {
	// Over a 1y horizon, cost = 2 * 1.5 = $3. gain = pos * drift / 100.
	// $1000 at 1pp drift = $10 > $3 → move. $200 at 1pp = $2 < $3 → skip.
	tests := []struct {
		name      string
		posUSD    float64
		cur, best float64
		horizon   time.Duration
		wantMove  bool
		wantWhy   string
	}{
		{"clear win", 1000, 4, 5, 365 * 24 * time.Hour, true, ReasonWorthIt},
		{"gain under gas", 200, 4, 5, 365 * 24 * time.Hour, false, ReasonNotWorthGas},
		{"exactly breakeven does not move", 300, 4, 5, 365 * 24 * time.Hour, false, ReasonNotWorthGas},
		{"big drift rescues a small position", 100, 2, 8, 365 * 24 * time.Hour, true, ReasonWorthIt},
		{"6h horizon prices almost nothing as worth it", 1000, 4, 8, 6 * time.Hour, false, ReasonNotWorthGas},
		{"6h horizon moves a whale", 5_000_000, 4, 8, 6 * time.Hour, true, ReasonWorthIt},
		{"negative drift never moves", 1000, 8, 4, 365 * 24 * time.Hour, false, ReasonDriftFloor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := base()
			p.Horizon = tt.horizon
			got := Evaluate(Input{
				PositionUSD: tt.posUSD, CurrentAPY: tt.cur, BestAPY: tt.best, Now: now,
			}, p)
			if got.Move != tt.wantMove || got.Reason != tt.wantWhy {
				t.Fatalf("move=%v reason=%q gain=%.4f cost=%.4f; want move=%v reason=%q",
					got.Move, got.Reason, got.GainUSD, got.CostUSD, tt.wantMove, tt.wantWhy)
			}
		})
	}
}

func TestEvaluateBreakevenNumbers(t *testing.T) {
	p := base()
	p.Horizon = 30 * 24 * time.Hour
	got := Evaluate(Input{PositionUSD: 10_000, CurrentAPY: 3, BestAPY: 6, Now: now}, p)
	// 10000 * 3 / 100 = $300/yr; 30/365 of that = $24.657.
	if math.Abs(got.GainUSD-24.6575) > 0.001 {
		t.Fatalf("gain %.4f", got.GainUSD)
	}
	if got.CostUSD != 3 {
		t.Fatalf("cost %.4f", got.CostUSD)
	}
	if got.Drift != 3 {
		t.Fatalf("drift %.4f", got.Drift)
	}
	if !got.Move {
		t.Fatal("should move")
	}
}

func TestEvaluateGuards(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want string
	}{
		{"tiny position", Input{PositionUSD: 49, CurrentAPY: 1, BestAPY: 20, Now: now}, ReasonPositionTiny},
		{"same venue short-circuits", Input{PositionUSD: 1e6, CurrentAPY: 1, BestAPY: 20, SameVenue: true, Now: now}, ReasonSameVenue},
		{"rate limited at the cap", Input{PositionUSD: 1e6, CurrentAPY: 1, BestAPY: 20, MovesLast24h: 4, Now: now}, ReasonRateLimited},
		{"under the cap is fine", Input{PositionUSD: 1e6, CurrentAPY: 1, BestAPY: 20, MovesLast24h: 3, Now: now}, ReasonWorthIt},
		{"drift under the absolute floor", Input{PositionUSD: 1e9, CurrentAPY: 4, BestAPY: 4.4, Now: now}, ReasonDriftFloor},
		{"drift exactly at the floor passes", Input{PositionUSD: 1e6, CurrentAPY: 4, BestAPY: 4.5, Now: now}, ReasonWorthIt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Evaluate(tt.in, base()); got.Reason != tt.want {
				t.Fatalf("reason %q want %q", got.Reason, tt.want)
			}
		})
	}
}

func TestEvaluateHysteresis(t *testing.T) {
	tests := []struct {
		name     string
		lastMove time.Time
		wantMove bool
		wantWhy  string
	}{
		{"never moved", time.Time{}, true, ReasonWorthIt},
		{"moved a minute ago", now.Add(-time.Minute), false, ReasonHoldPeriod},
		{"just inside the window", now.Add(-6*time.Hour + time.Second), false, ReasonHoldPeriod},
		{"exactly at the window", now.Add(-6 * time.Hour), true, ReasonWorthIt},
		{"long past the window", now.Add(-48 * time.Hour), true, ReasonWorthIt},
		// Clock skew between DB and keeper must not unlock the guard.
		{"future timestamp stays locked", now.Add(time.Hour), false, ReasonHoldPeriod},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(Input{
				PositionUSD: 100_000, CurrentAPY: 2, BestAPY: 9,
				LastMove: tt.lastMove, Now: now,
			}, base())
			if got.Move != tt.wantMove || got.Reason != tt.wantWhy {
				t.Fatalf("move=%v reason=%q; want %v %q", got.Move, got.Reason, tt.wantMove, tt.wantWhy)
			}
		})
	}
}

// The discount is what stops an emissions-heavy venue from looking better than
// a steady one. Checked through Evaluate, since that is where it bites.
func TestRewardDiscountChangesTheVerdict(t *testing.T) {
	p := base()
	p.Horizon = 365 * 24 * time.Hour
	current := EffectiveAPY(5, 0, 0, p.RewardDiscount)   // 5.0 steady
	candidate := EffectiveAPY(2, 7, 0, p.RewardDiscount) // 9.0 raw, 5.5 effective
	if raw := 2 + 7.0; raw <= current {
		t.Fatal("test setup: raw candidate should look better")
	}
	got := Evaluate(Input{PositionUSD: 1000, CurrentAPY: current, BestAPY: candidate, Now: now}, p)
	if got.Drift != 0.5 {
		t.Fatalf("discounted drift %.4f, want 0.5", got.Drift)
	}
	if !got.Move {
		t.Fatalf("0.5pp on $1000 over a year beats $3 gas; got %+v", got)
	}
	// Trusting rewards fully would have claimed a 4pp edge instead of 0.5pp.
	if undiscounted := EffectiveAPY(2, 7, 0, 1) - current; undiscounted != 4 {
		t.Fatalf("undiscounted drift %.4f", undiscounted)
	}
}

// The live mispricing intrinsic yield exists to fix: a user holding wstETH
// earns 2.26% from the staking rate plus 0.85% from lending it. Price the
// position at its lending leg alone and a 1.2% USDC venue looks like an
// upgrade, so the keeper moves them out of 3.11% into 1.2% and charges gas for
// it. With the intrinsic rate counted, the position is simply better and stays.
func TestIntrinsicYieldKeepsABetterPositionInPlace(t *testing.T) {
	p := base()
	// 0.85 -> 1.2 is a 0.35pp drift, under the default floor. Lower the floor so
	// the floor is not what saves the user here: the intrinsic rate has to.
	p.MinDriftAPY = 0.25
	const (
		lending   = 0.85 // wstETH supply APY on the lending venue
		staking   = 2.26 // wstETH exchange rate growth, measured
		usdcVenue = 1.2  // the "better" venue the keeper would have chosen
	)
	held := EffectiveAPY(lending, 0, staking, p.RewardDiscount)
	if held <= usdcVenue {
		t.Fatalf("test setup: held %.2f should beat the candidate %.2f", held, usdcVenue)
	}
	got := Evaluate(Input{
		Asset:       "WSTETH",
		PositionUSD: 100_000, // large enough that gas is never the reason
		CurrentAPY:  held,
		BestAPY:     EffectiveAPY(usdcVenue, 0, 0, p.RewardDiscount),
		Now:         now,
	}, p)
	if got.Move {
		t.Fatalf("moved a 3.11%% position into a 1.2%% one: %+v", got)
	}
	if got.Reason != ReasonDriftFloor {
		t.Fatalf("reason %q, want %q (drift is negative)", got.Reason, ReasonDriftFloor)
	}

	// And the bug itself, kept as the counter-example: blind to the staking
	// rate, the same inputs say "move".
	blind := Evaluate(Input{
		Asset:       "WSTETH",
		PositionUSD: 100_000,
		CurrentAPY:  EffectiveAPY(lending, 0, 0, p.RewardDiscount),
		BestAPY:     EffectiveAPY(usdcVenue, 0, 0, p.RewardDiscount),
		Now:         now,
	}, p)
	if !blind.Move {
		t.Fatal("test setup: without the intrinsic rate the keeper should want to move")
	}
}
