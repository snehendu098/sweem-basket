// Package engine runs the keeper's evaluation pass: read positions, price the
// alternatives, apply policy, and ask the wallet service to move what is worth
// moving. The keeper decides *when*; the wallet service still does *how*.
package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/client"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/policy"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/store"
)

// Deps are injected as interfaces so a pass can be driven against doubles.
type (
	Reader interface {
		ActiveSubscriptions(ctx context.Context) ([]store.Subscription, error)
		Positions(ctx context.Context, userID, basketID string) ([]store.Position, error)
		RebalanceHistory(ctx context.Context, userID string, since, now time.Time) (store.History, error)
		PendingExecutions(ctx context.Context, olderThan time.Time, limit int) ([]store.PendingExecution, error)
		ResolveExecution(ctx context.Context, id, status string, errMsg *string) (bool, error)
		UpsertPosition(ctx context.Context, p store.Position, userID, basketID, project string, entryAPY *float64) error
	}
	Market interface {
		Venues(ctx context.Context, asset, chain string, minTVL float64, limit int) ([]client.Venue, error)
	}
	Rebalancer interface {
		Rebalance(ctx context.Context, basketID, privyDID string) (client.RebalanceResult, error)
	}
	// Coster prices one rebalance in USD from live chain data, on the chain the
	// move would happen on — gas and ETH price are both per chain. It returns
	// an error rather than a guess when an input is missing or stale.
	Coster interface {
		CostUSD(ctx context.Context, chainID int) (float64, error)
	}
)

type Engine struct {
	Store    Reader
	Market   Market
	Wallet   Rebalancer
	Cost     Coster
	Receipts Receipts
	Log      *slog.Logger
	Policy   policy.Params

	MinVenueTVL float64
	DryRun      bool
	// PendingMinAge skips rows too young to have mined; PendingGiveUp is how
	// long an unobserved transaction is tolerated before it is called failed.
	PendingMinAge time.Duration
	PendingGiveUp time.Duration
	Now           func() time.Time // overridable in tests

	stats Stats
}

// Stats is the audit surface: what the last pass looked at and what it did.
type Stats struct {
	Passes        int            `json:"passes"`
	LastPassAt    time.Time      `json:"last_pass_at"`
	LastPassMS    int64          `json:"last_pass_ms"`
	Subscriptions int            `json:"subscriptions_evaluated"`
	LegsEvaluated int            `json:"legs_evaluated"`
	LegsMoved     int            `json:"legs_moved"`
	LegsSkipped   map[string]int `json:"legs_skipped_by_reason"`
	Errors        int            `json:"errors"`
	LastError     string         `json:"last_error,omitempty"`
	// GasCostUSD is the live cost the breakeven was priced on this pass, per
	// chain label. Two chains cost different amounts to move on, so one number
	// would be a number for the wrong chain half the time.
	GasCostUSD map[string]float64 `json:"gas_cost_usd"`
	Sweep      SweepStats         `json:"sweep"`
}

// SkipReasonCostUnavailable is recorded when the pass could not price a
// rebalance. No decision is taken: fabricated cost data is worse than none.
const SkipReasonCostUnavailable = "gas_cost_unavailable"

// SkipReasonUnknownChain is recorded when a position names a chain this
// backend does not serve. Routing it against the chain we happen to be
// configured for is how money lands on the wrong network.
const SkipReasonUnknownChain = "unknown_chain"

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Stats returns a copy of the last pass's counters.
func (e *Engine) Stats() Stats {
	s := e.stats
	s.LegsSkipped = map[string]int{}
	for k, v := range e.stats.LegsSkipped {
		s.LegsSkipped[k] = v
	}
	s.GasCostUSD = map[string]float64{}
	for k, v := range e.stats.GasCostUSD {
		s.GasCostUSD[k] = v
	}
	return s
}

// Pass evaluates every eligible subscription once. It never returns early on a
// single subscription's failure: one broken user must not stall the other 99.
func (e *Engine) Pass(ctx context.Context) Stats {
	start := e.now()
	st := Stats{Passes: e.stats.Passes + 1, LastPassAt: start,
		LegsSkipped: map[string]int{}, GasCostUSD: map[string]float64{}}

	// One venue lookup per (asset, chain) per pass, shared across subscribers
	// and with the sweeper.
	venues := map[string][]client.Venue{}

	// Resolve what already happened before deciding what to do next, or the
	// drift evaluation reasons about positions we know are stale.
	st.Sweep = e.sweepPending(ctx, venues)

	// Price the move on live data, per chain and on demand: a chain whose gas
	// or ETH price cannot be read skips its own legs, and never borrows the
	// other chain's cost. No fallback — a rebalance decided on fabricated cost
	// data is worse than no rebalance.
	costs := &costCache{chains: map[int]chainCost{}, engine: e, st: &st}

	subs, err := e.Store.ActiveSubscriptions(ctx)
	if err != nil {
		st.Errors++
		st.LastError = err.Error()
		e.Log.Error("pass: load subscriptions", "err", err)
		st.LastPassMS = e.now().Sub(start).Milliseconds()
		e.stats = st
		return st
	}

	// Per-user state for the pass: history is read once, and moves decided in
	// this pass count against the daily cap immediately, so a user subscribed
	// to several baskets cannot spend the budget twice.
	pass := &passState{hist: map[string]store.History{}, spent: map[string]int{}, params: e.Policy, costs: costs}

	for _, sub := range subs {
		st.Subscriptions++
		if err := e.evaluateSubscription(ctx, sub, venues, pass, &st); err != nil {
			st.Errors++
			st.LastError = err.Error()
			e.Log.Error("pass: subscription failed",
				"user_id", sub.UserID, "basket_id", sub.BasketID, "err", err)
		}
	}

	st.LastPassMS = e.now().Sub(start).Milliseconds()
	e.stats = st
	e.Log.Info("pass complete",
		"subscriptions", st.Subscriptions, "legs", st.LegsEvaluated,
		"moved", st.LegsMoved, "skipped", st.LegsSkipped,
		"gas_cost_usd", st.GasCostUSD, "sweep", st.Sweep,
		"errors", st.Errors, "ms", st.LastPassMS, "dry_run", e.DryRun)
	return st
}

// passState carries per-user state across the subscriptions of one pass.
type passState struct {
	hist   map[string]store.History
	spent  map[string]int
	params policy.Params
	costs  *costCache
}

// costCache prices one rebalance per chain, once per pass.
type costCache struct {
	chains map[int]chainCost
	engine *Engine
	st     *Stats
}

type chainCost struct {
	usd float64
	err error
}

// usd returns the cost of a move on one chain, or false if it cannot be had.
func (c *costCache) usd(ctx context.Context, chainID int, label string) (float64, bool) {
	got, ok := c.chains[chainID]
	if !ok {
		usd, err := c.engine.Cost.CostUSD(ctx, chainID)
		got = chainCost{usd: usd, err: err}
		c.chains[chainID] = got
		if err != nil {
			c.st.Errors++
			c.st.LastError = err.Error()
			c.engine.Log.Error("cannot price a rebalance on this chain; its legs are skipped",
				"chain", label, "chain_id", chainID, "reason", SkipReasonCostUnavailable, "err", err)
		} else {
			c.st.GasCostUSD[label] = usd
		}
	}
	if got.err != nil {
		return 0, false
	}
	return got.usd, true
}

func (e *Engine) evaluateSubscription(ctx context.Context, sub store.Subscription, venues map[string][]client.Venue, pass *passState, st *Stats) error {
	now := e.now()

	positions, err := e.Store.Positions(ctx, sub.UserID, sub.BasketID)
	if err != nil {
		return err
	}
	if len(positions) == 0 {
		return nil
	}

	// Read history back far enough to answer both the hysteresis and the
	// 24h rate-limit guard from one query.
	hist, ok := pass.hist[sub.UserID]
	if !ok {
		lookback := 24 * time.Hour
		if pass.params.MinHold > lookback {
			lookback = pass.params.MinHold
		}
		if hist, err = e.Store.RebalanceHistory(ctx, sub.UserID, now.Add(-lookback), now); err != nil {
			return err
		}
		pass.hist[sub.UserID] = hist
	}

	budget := hist.MovesLast24h + pass.spent[sub.UserID]
	move := false

	for _, p := range positions {
		st.LegsEvaluated++

		chain := p.Chain
		if chain == "" {
			chain = sub.Chain
		}
		chainID, known := chains.ID(chain)
		if !known {
			st.LegsSkipped[SkipReasonUnknownChain]++
			e.Log.Error("skip leg", "reason", SkipReasonUnknownChain, "user_id", sub.UserID,
				"asset", p.Asset, "chain", chain)
			continue
		}
		cost, priced := pass.costs.usd(ctx, chainID, chain)
		if !priced {
			st.LegsSkipped[SkipReasonCostUnavailable]++
			continue
		}
		// A copy per leg, so the live price never mutates the configured params
		// and one chain's cost never prices another chain's move.
		params := pass.params
		params.GasCostUSD = cost

		key := p.Asset + "@" + chain
		list, ok := venues[key]
		if !ok {
			list, err = e.Market.Venues(ctx, p.Asset, chain, e.MinVenueTVL, 20)
			if err != nil {
				return err
			}
			venues[key] = list
		}

		best, current, found := e.rank(list, p, params.RewardDiscount)
		if !found {
			st.LegsSkipped["no_venue"]++
			e.Log.Info("skip leg", "reason", "no_venue", "user_id", sub.UserID,
				"asset", p.Asset, "chain", chain)
			continue
		}

		d := policy.Evaluate(policy.Input{
			Asset:        p.Asset,
			PositionUSD:  p.AmountUSD,
			CurrentAPY:   current,
			BestAPY:      best.APY,
			SameVenue:    best.ID == p.VenueID,
			LastMove:     hist.LastMove[p.Asset],
			MovesLast24h: budget,
			Now:          now,
		}, params)

		e.Log.Info("leg decision",
			"move", d.Move, "reason", d.Reason,
			"user_id", sub.UserID, "basket_id", sub.BasketID, "asset", p.Asset,
			"position_usd", p.AmountUSD,
			"from_venue", p.VenueID, "to_venue", best.ID,
			"current_apy", current, "best_apy", best.APY, "drift", d.Drift,
			"gain_usd", d.GainUSD, "cost_usd", d.CostUSD,
			"moves_last_24h", budget, "last_move", hist.LastMove[p.Asset])

		if !d.Move {
			st.LegsSkipped[d.Reason]++
			continue
		}
		move = true
		budget++
		pass.spent[sub.UserID]++
		st.LegsMoved++
	}

	if !move {
		return nil
	}
	if e.DryRun {
		e.Log.Info("dry run: would rebalance",
			"user_id", sub.UserID, "privy_did", sub.PrivyDID, "basket_id", sub.BasketID)
		return nil
	}

	res, err := e.Wallet.Rebalance(ctx, sub.BasketID, sub.PrivyDID)
	if err != nil {
		return err
	}
	e.Log.Info("rebalance submitted",
		"user_id", sub.UserID, "basket_id", sub.BasketID,
		"moved_legs", res.MovedLegs, "failed_legs", res.FailedLegs)
	return nil
}

// rank picks the best venue by reward-discounted APY and prices the position's
// current venue the same way. Falling back to entry_apy when the current venue
// has dropped out of market-data is the conservative choice: a stale, usually
// higher number makes the keeper less eager to move, not more.
func (e *Engine) rank(list []client.Venue, p store.Position, discount float64) (best client.Venue, currentAPY float64, found bool) {
	currentAPY = policy.EffectiveAPY(p.EntryAPY, 0, 0, discount)
	for _, v := range list {
		eff := policy.EffectiveAPY(v.APYBase, v.APYReward, v.APYIntrinsic, discount)
		if v.ID == p.VenueID {
			currentAPY = eff
		}
		if !found || eff > best.APY {
			best, found = v, true
			best.APY = eff // carry the effective rate, not the raw one
		}
	}
	return best, currentAPY, found
}
