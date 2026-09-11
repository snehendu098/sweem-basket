package api

import (
	"math"
	"testing"

	"github.com/snehendu098/sweem-basket/services/wallet/internal/store"
)

// A withdrawal must take exactly what was asked for, spread across positions in
// proportion to what each holds.
func TestSplitProportional(t *testing.T) {
	tests := []struct {
		name   string
		amount float64
		values []float64
		want   []float64
	}{
		{
			name: "even halves", amount: 100, values: []float64{600, 600},
			want: []float64{50, 50},
		},
		{
			name: "proportional to holdings, not equal", amount: 100, values: []float64{750, 250},
			want: []float64{75, 25},
		},
		{
			name: "full exit takes every position whole", amount: 1000, values: []float64{600, 400},
			want: []float64{600, 400},
		},
		{
			// 100/3 does not divide evenly; the leftover cent must land
			// somewhere rather than evaporate.
			name: "thirds", amount: 100, values: []float64{100, 100, 100},
			want: []float64{33.34, 33.33, 33.33},
		},
		{
			name: "lopsided holdings", amount: 10, values: []float64{9999, 1},
			want: []float64{10, 0},
		},
		{
			name: "a zero-value position draws nothing", amount: 100, values: []float64{100, 0},
			want: []float64{100, 0},
		},
		{
			name: "single position", amount: 42.42, values: []float64{500},
			want: []float64{42.42},
		},
		{
			name: "no positions", amount: 100, values: []float64{},
			want: []float64{},
		},
		{
			name: "nothing requested", amount: 0, values: []float64{100, 100},
			want: []float64{0, 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitProportional(tt.amount, tt.values)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d legs, want %d", len(got), len(tt.want))
			}
			var sum float64
			for i := range got {
				if math.Abs(got[i]-tt.want[i]) > 1e-9 {
					t.Errorf("leg %d = %v, want %v", i, got[i], tt.want[i])
				}
				sum += got[i]
			}
			// The invariant: the legs sum back to the requested amount.
			if len(tt.values) > 0 && math.Abs(sum-tt.amount) > 0.01 {
				t.Errorf("legs sum to %v, want %v", sum, tt.amount)
			}
		})
	}
}

// No amount or distribution of holdings may leak value.
func TestSplitProportionalSumsToTotal(t *testing.T) {
	valueSets := [][]float64{
		{100, 100, 100},
		{1234.56, 7.89, 0.01},
		{1, 1, 1, 1, 1, 1, 7},
		{9999.99, 0.01},
	}
	for _, values := range valueSets {
		var held float64
		for _, v := range values {
			held += v
		}
		for _, amount := range []float64{0.03, 1, 33.33, 100.01, held} {
			var sum float64
			for _, v := range splitProportional(amount, values) {
				sum += v
			}
			if math.Abs(sum-amount) > 0.01 {
				t.Errorf("values %v amount %v: legs sum to %v", values, amount, sum)
			}
		}
	}
}

// all:true is the full held value, so every position is drained exactly.
func TestSplitProportionalFullExit(t *testing.T) {
	values := []float64{612.33, 287.11, 100.56}
	var total float64
	for _, v := range values {
		total += v
	}
	got := splitProportional(total, values)
	for i := range values {
		if math.Abs(got[i]-values[i]) > 1e-9 {
			t.Errorf("position %d: withdrawing %v, want its whole %v", i, got[i], values[i])
		}
	}
}

// allocate is shared with the deposit path; both callers depend on the sum
// being exact and on weights of zero drawing nothing.
func TestAllocate(t *testing.T) {
	tests := []struct {
		name    string
		total   int64
		weights []int64
		want    []int64
	}{
		{name: "exact halves", total: 100, weights: []int64{5000, 5000}, want: []int64{50, 50}},
		{name: "leftover to the biggest remainder", total: 100, weights: []int64{3333, 3333, 3334}, want: []int64{33, 33, 34}},
		{name: "zero weight gets nothing", total: 100, weights: []int64{100, 0}, want: []int64{100, 0}},
		{name: "negative weight is ignored", total: 100, weights: []int64{100, -5}, want: []int64{100, 0}},
		{name: "no total", total: 0, weights: []int64{1, 1}, want: []int64{0, 0}},
		{name: "no weight at all", total: 100, weights: []int64{0, 0}, want: []int64{0, 0}},
		{name: "negative total", total: -100, weights: []int64{1, 1}, want: []int64{0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := allocate(tt.total, tt.weights)
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("allocate(%d, %v)[%d] = %d, want %d", tt.total, tt.weights, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// A position whose funds are already idle in the wallet must be skipped, not
// routed at a venue that is not holding the money — and it must not absorb any
// of the requested amount.
func TestPartitionWithdrawable(t *testing.T) {
	tests := []struct {
		name          string
		positions     []store.Position
		wantWithdraw  int
		wantSkipped   int
		wantAvailable float64
	}{
		{
			name: "all in venues",
			positions: []store.Position{
				{Asset: "USDC", VenueID: "Base:moonwell:0xaa", AmountUSD: 600},
				{Asset: "WETH", VenueID: "Base:aave-v3:0xbb", AmountUSD: 400},
			},
			wantWithdraw: 2, wantAvailable: 1000,
		},
		{
			name: "an idle position is skipped and excluded from the total",
			positions: []store.Position{
				{Asset: "USDC", VenueID: "Base:moonwell:0xaa", AmountUSD: 600},
				{Asset: "WETH", VenueID: IdleVenueID, AmountUSD: 400},
			},
			wantWithdraw: 1, wantSkipped: 1, wantAvailable: 600,
		},
		{
			name: "everything idle leaves nothing to withdraw",
			positions: []store.Position{
				{Asset: "USDC", VenueID: IdleVenueID, AmountUSD: 600},
			},
			wantSkipped: 1,
		},
		{name: "no positions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, skipped, available := partitionWithdrawable(tt.positions)
			if len(got) != tt.wantWithdraw {
				t.Errorf("withdrawable = %d, want %d", len(got), tt.wantWithdraw)
			}
			if len(skipped) != tt.wantSkipped {
				t.Errorf("skipped = %d, want %d", len(skipped), tt.wantSkipped)
			}
			if math.Abs(available-tt.wantAvailable) > 1e-9 {
				t.Errorf("available = %v, want %v", available, tt.wantAvailable)
			}
			for _, sk := range skipped {
				if sk.Status != legSkipped {
					t.Errorf("skipped leg status = %q, want %q", sk.Status, legSkipped)
				}
				if sk.Reason == "" {
					t.Error("skipped leg has no reason")
				}
				if sk.AmountUSD != 0 {
					t.Errorf("skipped leg claims %v withdrawn, want 0", sk.AmountUSD)
				}
			}
		})
	}
}
