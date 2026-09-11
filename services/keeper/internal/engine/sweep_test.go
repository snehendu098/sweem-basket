package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/snehendu098/sweem-basket/services/keeper/internal/client"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/rpc"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/store"
)

func pendingRow(age time.Duration) store.PendingExecution {
	return store.PendingExecution{
		ID: "e1", UserID: "u1", BasketID: "b1", Kind: "rebalance", Asset: "USDC",
		ToVenue: "Base:moonwell:pool", AmountUSD: 1234.5, TxHash: "0xabc",
		CreatedAt: now.Add(-age),
	}
}

func sweepEngine(t *testing.T, row *store.PendingExecution, r *fakeReceipts) (*Engine, *fakeStore) {
	t.Helper()
	s, m, w := fixtures()
	if row != nil {
		s.pending = []store.PendingExecution{*row}
	}
	e := newEngine(s, m, w)
	e.Receipts = r
	return e, s
}

func TestSweep(t *testing.T) {
	mined := &rpc.Receipt{Status: "0x1"}
	reverted := &rpc.Receipt{Status: "0x0"}

	tests := []struct {
		name          string
		age           time.Duration
		receipt       *rpc.Receipt
		wantResolved  string // "" = row left pending
		wantPositions int
		want          SweepStats
	}{
		{
			name: "too young to check", age: time.Minute, receipt: mined,
			want: SweepStats{},
		},
		{
			name: "mined confirms and writes the position", age: 10 * time.Minute, receipt: mined,
			wantResolved: "confirmed", wantPositions: 1,
			want: SweepStats{Checked: 1, Confirmed: 1},
		},
		{
			name: "reverted fails and writes nothing", age: 10 * time.Minute, receipt: reverted,
			wantResolved: "failed",
			want:         SweepStats{Checked: 1, Failed: 1},
		},
		{
			name: "unmined stays pending", age: 10 * time.Minute, receipt: nil,
			want: SweepStats{Checked: 1, StillPending: 1},
		},
		{
			name: "unobserved for a day is given up on", age: 25 * time.Hour, receipt: nil,
			wantResolved: "failed",
			want:         SweepStats{Checked: 1, GivenUp: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := pendingRow(tt.age)
			r := &fakeReceipts{byHash: map[string]*rpc.Receipt{}}
			if tt.receipt != nil {
				r.byHash[row.TxHash] = tt.receipt
			}
			e, s := sweepEngine(t, &row, r)

			got := e.sweepPending(context.Background(), map[string][]client.Venue{})
			if got != tt.want {
				t.Fatalf("stats %+v want %+v", got, tt.want)
			}
			switch {
			case tt.wantResolved == "" && len(s.resolved) != 0:
				t.Fatalf("resolved %+v, want none", s.resolved)
			case tt.wantResolved != "":
				if len(s.resolved) != 1 || s.resolved[0].status != tt.wantResolved {
					t.Fatalf("resolved %+v want %q", s.resolved, tt.wantResolved)
				}
			}
			if len(s.upserts) != tt.wantPositions {
				t.Fatalf("positions %+v want %d", s.upserts, tt.wantPositions)
			}
		})
	}
}

// The give-up path must say, in the row, that the transaction was never seen.
func TestSweepGiveUpRecordsWhy(t *testing.T) {
	row := pendingRow(25 * time.Hour)
	e, s := sweepEngine(t, &row, &fakeReceipts{byHash: map[string]*rpc.Receipt{}})
	e.sweepPending(context.Background(), map[string][]client.Venue{})

	if len(s.resolved) != 1 || s.resolved[0].errMsg == nil {
		t.Fatalf("resolved %+v", s.resolved)
	}
	if msg := *s.resolved[0].errMsg; msg == "" || !contains(msg, "never observed") || !contains(msg, row.TxHash) {
		t.Fatalf("error message %q", msg)
	}
}

// The venue ID carries the chain and project the executions row does not.
func TestSweepWritesPositionFromVenueID(t *testing.T) {
	row := pendingRow(10 * time.Minute)
	r := &fakeReceipts{byHash: map[string]*rpc.Receipt{row.TxHash: {Status: "0x1"}}}
	e, s := sweepEngine(t, &row, r)
	e.sweepPending(context.Background(), map[string][]client.Venue{})

	got := s.upserts[0]
	want := upsert{userID: "u1", basketID: "b1", asset: "USDC", venueID: "Base:moonwell:pool",
		chain: "Base", project: "moonwell", amountUSD: 1234.5}
	if got.entryAPY == nil || *got.entryAPY != 6 { // live APY of that venue, from market-data
		t.Fatalf("entry apy %v", got.entryAPY)
	}
	got.entryAPY = nil
	if got != want {
		t.Fatalf("upsert %+v want %+v", got, want)
	}
}

// market-data not carrying the venue must not produce an invented APY.
func TestSweepLeavesEntryAPYAloneWhenVenueUnknown(t *testing.T) {
	row := pendingRow(10 * time.Minute)
	row.ToVenue = "Base:unknown:0xpool"
	r := &fakeReceipts{byHash: map[string]*rpc.Receipt{row.TxHash: {Status: "0x1"}}}
	e, s := sweepEngine(t, &row, r)
	e.sweepPending(context.Background(), map[string][]client.Venue{})

	if s.upserts[0].entryAPY != nil {
		t.Fatalf("entry apy %v, want nil", *s.upserts[0].entryAPY)
	}
}

// Two runs over the same row — a second pass, or a second keeper instance —
// must resolve and write it exactly once.
func TestSweepIsIdempotent(t *testing.T) {
	row := pendingRow(10 * time.Minute)
	r := &fakeReceipts{byHash: map[string]*rpc.Receipt{row.TxHash: {Status: "0x1"}}}
	e, s := sweepEngine(t, &row, r)

	first := e.sweepPending(context.Background(), map[string][]client.Venue{})
	second := e.sweepPending(context.Background(), map[string][]client.Venue{})

	if first.Confirmed != 1 || second.Confirmed != 0 {
		t.Fatalf("first %+v second %+v", first, second)
	}
	if len(s.resolved) != 1 {
		t.Fatalf("resolved %+v", s.resolved)
	}
	if len(s.upserts) != 1 {
		t.Fatalf("positions written %d, want 1", len(s.upserts))
	}
}

func TestSweepDryRunChangesNothing(t *testing.T) {
	row := pendingRow(10 * time.Minute)
	r := &fakeReceipts{byHash: map[string]*rpc.Receipt{row.TxHash: {Status: "0x1"}}}
	e, s := sweepEngine(t, &row, r)
	e.DryRun = true

	got := e.sweepPending(context.Background(), map[string][]client.Venue{})
	if got.Confirmed != 1 {
		t.Fatalf("dry run must still decide: %+v", got)
	}
	if len(s.resolved) != 0 || len(s.upserts) != 0 {
		t.Fatalf("dry run wrote: resolved %+v upserts %+v", s.resolved, s.upserts)
	}
}

func TestSweepReceiptErrorLeavesRowAlone(t *testing.T) {
	row := pendingRow(10 * time.Minute)
	e, s := sweepEngine(t, &row, &fakeReceipts{err: errors.New("node down")})

	got := e.sweepPending(context.Background(), map[string][]client.Venue{})
	if got.Errors != 1 || got.Confirmed != 0 || len(s.resolved) != 0 {
		t.Fatalf("stats %+v resolved %+v", got, s.resolved)
	}
}

// The sweeper runs as part of the pass, before any drift decision.
func TestPassRunsSweepFirst(t *testing.T) {
	row := pendingRow(10 * time.Minute)
	r := &fakeReceipts{byHash: map[string]*rpc.Receipt{row.TxHash: {Status: "0x1"}}}
	e, s := sweepEngine(t, &row, r)

	st := e.Pass(context.Background())
	if st.Sweep.Confirmed != 1 {
		t.Fatalf("sweep stats %+v", st.Sweep)
	}
	if len(s.upserts) != 1 {
		t.Fatalf("positions %+v", s.upserts)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
