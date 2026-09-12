package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/snehendu098/sweem-basket/services/keeper/internal/client"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/policy"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/rpc"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/store"
)

var now = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	subs      []store.Subscription
	positions map[string][]store.Position // userID|basketID
	hist      store.History
	histCalls int

	pending    []store.PendingExecution
	resolved   []resolution
	upserts    []upsert
	claimed    map[string]bool // executions already taken, by id
	resolveErr error
}

type resolution struct {
	id, status string
	errMsg     *string
}

type upsert struct {
	userID, basketID, asset, venueID, chain, project string
	amountUSD                                        float64
	entryAPY                                         *float64
}

func (f *fakeStore) PendingExecutions(_ context.Context, olderThan time.Time, _ int) ([]store.PendingExecution, error) {
	var out []store.PendingExecution
	for _, p := range f.pending {
		if !p.CreatedAt.After(olderThan) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) ResolveExecution(_ context.Context, id, status string, errMsg *string) (bool, error) {
	if f.resolveErr != nil {
		return false, f.resolveErr
	}
	if f.claimed == nil {
		f.claimed = map[string]bool{}
	}
	if f.claimed[id] { // someone already moved it out of pending
		return false, nil
	}
	f.claimed[id] = true
	f.resolved = append(f.resolved, resolution{id, status, errMsg})
	return true, nil
}

func (f *fakeStore) UpsertPosition(_ context.Context, p store.Position, userID, basketID, project string, entryAPY *float64) error {
	f.upserts = append(f.upserts, upsert{userID, basketID, p.Asset, p.VenueID, p.Chain, project, p.AmountUSD, entryAPY})
	return nil
}

type fakeCost struct {
	usd float64
	err error
}

func (f fakeCost) CostUSD(context.Context, int) (float64, error) { return f.usd, f.err }

type fakeReceipts struct {
	byHash map[string]*rpc.Receipt
	err    error
	calls  int
}

func (f *fakeReceipts) TransactionReceipt(_ context.Context, _ int, h string) (*rpc.Receipt, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.byHash[h], nil
}

func (f *fakeStore) ActiveSubscriptions(context.Context) ([]store.Subscription, error) {
	return f.subs, nil
}
func (f *fakeStore) Positions(_ context.Context, u, b string) ([]store.Position, error) {
	return f.positions[u+"|"+b], nil
}
func (f *fakeStore) RebalanceHistory(context.Context, string, time.Time, time.Time) (store.History, error) {
	f.histCalls++
	if f.hist.LastMove == nil {
		f.hist.LastMove = map[string]time.Time{}
	}
	return f.hist, nil
}

type fakeMarket struct {
	venues []client.Venue
	calls  int
}

func (f *fakeMarket) Venues(context.Context, string, string, float64, int) ([]client.Venue, error) {
	f.calls++
	return f.venues, nil
}

type fakeWallet struct{ calls []string }

func (f *fakeWallet) Rebalance(_ context.Context, basketID, did string) (client.RebalanceResult, error) {
	f.calls = append(f.calls, did+"/"+basketID)
	return client.RebalanceResult{MovedLegs: 1}, nil
}

func newEngine(s *fakeStore, m *fakeMarket, w *fakeWallet) *Engine {
	return &Engine{
		Store: s, Market: m, Wallet: w,
		Cost:          fakeCost{usd: 0.5},
		Receipts:      &fakeReceipts{},
		PendingMinAge: 2 * time.Minute,
		PendingGiveUp: 24 * time.Hour,
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Policy: policy.Params{
			MinDriftAPY: 0.5, MinPositionUSD: 50, MinHold: 6 * time.Hour,
			RewardDiscount: 0.5, SafetyMargin: 1.5,
			MaxPerDay: 4, Horizon: 365 * 24 * time.Hour,
		},
		Now: func() time.Time { return now },
	}
}

func fixtures() (*fakeStore, *fakeMarket, *fakeWallet) {
	s := &fakeStore{
		subs: []store.Subscription{{UserID: "u1", PrivyDID: "did:privy:u1", BasketID: "b1", Chain: "base"}},
		positions: map[string][]store.Position{
			"u1|b1": {{Asset: "USDC", VenueID: "base:aave-v3:pool", Chain: "base", AmountUSD: 5000, EntryAPY: 3}},
		},
	}
	m := &fakeMarket{venues: []client.Venue{
		{ID: "base:aave-v3:pool", APYBase: 3, APY: 3},
		{ID: "base:moonwell:pool", APYBase: 6, APY: 6},
	}}
	return s, m, &fakeWallet{}
}

func TestPassSubmitsRebalance(t *testing.T) {
	s, m, w := fixtures()
	st := newEngine(s, m, w).Pass(context.Background())

	if st.LegsMoved != 1 || st.Subscriptions != 1 || st.Errors != 0 {
		t.Fatalf("stats %+v", st)
	}
	if len(w.calls) != 1 || w.calls[0] != "did:privy:u1/b1" {
		t.Fatalf("wallet calls %v", w.calls)
	}
}

func TestPassDryRunCallsNothing(t *testing.T) {
	s, m, w := fixtures()
	e := newEngine(s, m, w)
	e.DryRun = true
	st := e.Pass(context.Background())

	if st.LegsMoved != 1 {
		t.Fatalf("dry run must still decide: %+v", st)
	}
	if len(w.calls) != 0 {
		t.Fatalf("dry run called the wallet service: %v", w.calls)
	}
}

// The keeper must not move funds into the venue they are already in, however
// good that venue's rate looks.
func TestPassSkipsWhenAlreadyBest(t *testing.T) {
	s, m, w := fixtures()
	s.positions["u1|b1"][0].VenueID = "base:moonwell:pool"
	st := newEngine(s, m, w).Pass(context.Background())

	if st.LegsMoved != 0 || st.LegsSkipped[policy.ReasonSameVenue] != 1 {
		t.Fatalf("stats %+v", st)
	}
	if len(w.calls) != 0 {
		t.Fatalf("wallet called: %v", w.calls)
	}
}

// Reward APY is discounted before ranking, so a venue that only wins on
// emissions must not win here: 3 base beats 1 base + 3 reward (= 2.5).
func TestPassRanksOnDiscountedAPY(t *testing.T) {
	s, m, w := fixtures()
	m.venues = []client.Venue{
		{ID: "base:aave-v3:pool", APYBase: 3, APY: 3},
		{ID: "base:farm:pool", APYBase: 1, APYReward: 3, APY: 4},
	}
	st := newEngine(s, m, w).Pass(context.Background())

	if st.LegsMoved != 0 || st.LegsSkipped[policy.ReasonSameVenue] != 1 {
		t.Fatalf("emissions venue should not have won: %+v", st)
	}
}

// A user subscribed to several baskets must not blow past the daily cap by
// spreading moves across them inside one pass.
func TestPassRateLimitBudgetIsSharedAcrossBaskets(t *testing.T) {
	s, m, w := fixtures()
	s.subs = append(s.subs, store.Subscription{UserID: "u1", PrivyDID: "did:privy:u1", BasketID: "b2", Chain: "base"})
	s.positions["u1|b2"] = []store.Position{
		{Asset: "USDC", VenueID: "base:aave-v3:pool", Chain: "base", AmountUSD: 5000, EntryAPY: 3},
	}
	s.hist = store.History{LastMove: map[string]time.Time{}, MovesLast24h: 3} // one left

	st := newEngine(s, m, w).Pass(context.Background())
	if st.LegsMoved != 1 || st.LegsSkipped[policy.ReasonRateLimited] != 1 {
		t.Fatalf("stats %+v", st)
	}
	if len(w.calls) != 1 {
		t.Fatalf("wallet calls %v", w.calls)
	}
	// Venue data and history are each read once per pass, not per subscription.
	if m.calls != 1 {
		t.Fatalf("market calls %d, want 1", m.calls)
	}
	if s.histCalls != 1 {
		t.Fatalf("history calls %d, want 1", s.histCalls)
	}
}

func TestPassRespectsHoldPeriod(t *testing.T) {
	s, m, w := fixtures()
	s.hist = store.History{LastMove: map[string]time.Time{"USDC": now.Add(-time.Hour)}}
	st := newEngine(s, m, w).Pass(context.Background())

	if st.LegsMoved != 0 || st.LegsSkipped[policy.ReasonHoldPeriod] != 1 || len(w.calls) != 0 {
		t.Fatalf("stats %+v calls %v", st, w.calls)
	}
}

// entry_apy is the fallback when the current venue has dropped out of
// market-data — stale and usually high, which biases toward not moving.
func TestPassFallsBackToEntryAPY(t *testing.T) {
	s, m, w := fixtures()
	s.positions["u1|b1"][0].EntryAPY = 20
	m.venues = []client.Venue{{ID: "base:other:pool", APYBase: 6, APY: 6}}
	st := newEngine(s, m, w).Pass(context.Background())

	if st.LegsMoved != 0 || st.LegsSkipped[policy.ReasonDriftFloor] != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// A pass that cannot price a rebalance must decide nothing at all: acting on a
// fabricated gas cost is worse than not acting.
func TestPassSkipsWhenCostUnavailable(t *testing.T) {
	s, m, w := fixtures()
	e := newEngine(s, m, w)
	e.Cost = fakeCost{err: errors.New("rpc down")}
	st := e.Pass(context.Background())

	// The leg is seen but decided on nothing: no move, no call to the wallet.
	if st.LegsMoved != 0 || len(w.calls) != 0 {
		t.Fatalf("stats %+v calls %v", st, w.calls)
	}
	if st.LegsSkipped[SkipReasonCostUnavailable] != 1 || st.Errors != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// The live cost, not the configured one, is what the breakeven is priced on.
func TestPassUsesLiveCost(t *testing.T) {
	s, m, w := fixtures()
	e := newEngine(s, m, w)
	e.Policy.Horizon = 365 * 24 * time.Hour
	// $5000 at 3pp drift over a year is $150 of gain; $200 of gas beats it.
	e.Cost = fakeCost{usd: 200}
	st := e.Pass(context.Background())

	if st.GasCostUSD["base"] != 200 {
		t.Fatalf("cost %v", st.GasCostUSD)
	}
	if st.LegsMoved != 0 || st.LegsSkipped[policy.ReasonNotWorthGas] != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// A position naming a chain this backend does not serve is skipped loudly. The
// alternative — pricing and routing it against whichever chain happens to be
// configured — puts money on the wrong network.
func TestPassSkipsPositionsOnAnUnknownChain(t *testing.T) {
	s, m, w := fixtures()
	s.positions["u1|b1"] = []store.Position{
		{Asset: "USDC", VenueID: "arbitrum:aave-v3:pool", Chain: "arbitrum", AmountUSD: 5000, EntryAPY: 3},
	}
	e := newEngine(s, m, w)
	st := e.Pass(context.Background())

	if st.LegsSkipped[SkipReasonUnknownChain] != 1 {
		t.Fatalf("stats %+v", st)
	}
	if st.LegsMoved != 0 || len(w.calls) != 0 {
		t.Fatalf("moved money on an unknown chain: %+v %v", st, w.calls)
	}
}

// Each chain is priced on its own estimator: a leg is never costed with the
// other chain's gas.
func TestPassPricesEachChainSeparately(t *testing.T) {
	s, m, w := fixtures()
	s.positions["u1|b1"] = []store.Position{
		{Asset: "USDC", VenueID: "base:aave-v3:pool", Chain: "base", AmountUSD: 5000, EntryAPY: 3},
		{Asset: "USDC", VenueID: "base-sepolia:aave-v3:pool", Chain: "base-sepolia", AmountUSD: 5000, EntryAPY: 3},
	}
	e := newEngine(s, m, w)
	e.Cost = perChainCost{8453: 0.5, 84532: 0.25}
	st := e.Pass(context.Background())

	if st.GasCostUSD["base"] != 0.5 || st.GasCostUSD["base-sepolia"] != 0.25 {
		t.Fatalf("per-chain cost = %v", st.GasCostUSD)
	}
}

type perChainCost map[int]float64

func (p perChainCost) CostUSD(_ context.Context, chainID int) (float64, error) {
	usd, ok := p[chainID]
	if !ok {
		return 0, errors.New("no estimator for chain")
	}
	return usd, nil
}
