package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/prices"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/executor"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/tokenapi"

	"github.com/snehendu098/sweem-basket/services/wallet/internal/marketdata"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/store"
)

func TestSplitAmounts(t *testing.T) {
	tests := []struct {
		name    string
		amount  float64
		weights []store.Weight
		want    []float64
	}{
		{
			name:    "even split",
			amount:  1000,
			weights: []store.Weight{{Asset: "USDC", WeightBps: 6000}, {Asset: "WETH", WeightBps: 4000}},
			want:    []float64{600, 400},
		},
		{
			name:   "thirds do not divide evenly",
			amount: 100,
			weights: []store.Weight{
				{Asset: "A", WeightBps: 3333}, {Asset: "B", WeightBps: 3333}, {Asset: "C", WeightBps: 3334},
			},
			want: []float64{33.33, 33.33, 33.34},
		},
		{
			name:   "leftover cents go to the biggest remainders",
			amount: 10.01,
			weights: []store.Weight{
				{Asset: "A", WeightBps: 3333}, {Asset: "B", WeightBps: 3333}, {Asset: "C", WeightBps: 3334},
			},
			// 1001 cents: each leg floors to 333, two cents left over; C has the
			// biggest remainder, then A wins the tie with B by position.
			want: []float64{3.34, 3.33, 3.34},
		},
		{
			name:    "sub-cent leg rounds to zero",
			amount:  0.10,
			weights: []store.Weight{{Asset: "A", WeightBps: 9999}, {Asset: "B", WeightBps: 1}},
			want:    []float64{0.10, 0},
		},
		{
			name:    "single asset takes it all",
			amount:  1234.56,
			weights: []store.Weight{{Asset: "USDC", WeightBps: 10000}},
			want:    []float64{1234.56},
		},
		{
			name:    "zero amount",
			amount:  0,
			weights: []store.Weight{{Asset: "A", WeightBps: 10000}},
			want:    []float64{0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitAmounts(tt.amount, tt.weights)
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
			// The invariant that matters: no cent is created or lost.
			if math.Abs(sum-tt.amount) > 0.01 {
				t.Errorf("legs sum to %v, want %v (deposit total)", sum, tt.amount)
			}
		})
	}
}

// A deposit must never leak value regardless of how awkward the weights are.
func TestSplitAmountsSumsToTotal(t *testing.T) {
	weights := []store.Weight{
		{Asset: "A", WeightBps: 1667}, {Asset: "B", WeightBps: 1667}, {Asset: "C", WeightBps: 1666},
		{Asset: "D", WeightBps: 1667}, {Asset: "E", WeightBps: 1667}, {Asset: "F", WeightBps: 1666},
	}
	for _, amount := range []float64{0.07, 1, 33.33, 100.01, 999.99, 1_000_000.55} {
		var sum float64
		for _, v := range splitAmounts(amount, weights) {
			sum += v
		}
		if math.Abs(sum-amount) > 0.01 {
			t.Errorf("amount %v: legs sum to %v", amount, sum)
		}
	}
}

func TestDriftAPY(t *testing.T) {
	pos := store.Position{VenueID: "Base:moonwell:0xaa", EntryAPY: 4.0}
	tests := []struct {
		name        string
		best        marketdata.Venue
		wantCurrent float64
		wantDrift   float64
	}{
		{"already in best venue uses the live rate", marketdata.Venue{ID: "Base:moonwell:0xaa", APY: 6.5}, 6.5, 0},
		{"better venue elsewhere", marketdata.Venue{ID: "Base:aave-v3:0xbb", APY: 6.5}, 4.0, 2.5},
		{"worse venue elsewhere gives negative drift", marketdata.Venue{ID: "Base:aave-v3:0xbb", APY: 3.0}, 4.0, -1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur, drift := driftAPY(pos, tt.best)
			if cur != tt.wantCurrent || drift != tt.wantDrift {
				t.Errorf("got (%v, %v), want (%v, %v)", cur, drift, tt.wantCurrent, tt.wantDrift)
			}
		})
	}
}

func TestShouldRebalance(t *testing.T) {
	const threshold = 0.5
	tests := []struct {
		name  string
		drift float64
		want  bool
	}{
		{"clearly above threshold", 2.0, true},
		{"exactly at threshold stays put", 0.5, false},
		{"just under threshold stays put", 0.49, false},
		{"just over threshold moves", 0.51, true},
		{"negative drift never moves", -3.0, false},
		{"no drift never moves", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRebalance(tt.drift, threshold); got != tt.want {
				t.Errorf("shouldRebalance(%v, %v) = %v, want %v", tt.drift, threshold, got, tt.want)
			}
		})
	}
}

func TestLegOutcome(t *testing.T) {
	tests := []struct {
		name          string
		resp          executor.RouteResponse
		err           error
		wantDBStatus  string
		wantLegStatus string
		wantReason    string
	}{
		{
			name:          "transport or 502 error is a failure",
			resp:          executor.RouteResponse{Status: "failed"},
			err:           errors.New("executor: status 502: step 1 reverted"),
			wantDBStatus:  "failed",
			wantLegStatus: legFailed,
			wantReason:    "executor: status 502: step 1 reverted",
		},
		{
			name:          "receipt poll timeout stays pending, never failed",
			resp:          executor.RouteResponse{Status: "pending", TxHash: "0xabc"},
			wantDBStatus:  "pending",
			wantLegStatus: legPending,
			wantReason:    "submitted but not yet confirmed; receipt poll timed out",
		},
		{
			name:          "confirmed",
			resp:          executor.RouteResponse{Status: "confirmed", TxHash: "0xabc"},
			wantDBStatus:  "confirmed",
			wantLegStatus: legSubmitted,
		},
		{
			name:          "submitted",
			resp:          executor.RouteResponse{Status: "submitted", TxHash: "0xabc"},
			wantDBStatus:  "submitted",
			wantLegStatus: legSubmitted,
		},
		{
			name:          "status failed on a 200 still fails",
			resp:          executor.RouteResponse{Status: "failed", Error: "slippage exceeded"},
			wantDBStatus:  "failed",
			wantLegStatus: legFailed,
			wantReason:    "slippage exceeded",
		},
		{
			name:          "failed with no message still explains itself",
			resp:          executor.RouteResponse{Status: "failed"},
			wantDBStatus:  "failed",
			wantLegStatus: legFailed,
			wantReason:    "executor reported failure",
		},
		{
			name:          "unknown status is treated as submitted, not lost",
			resp:          executor.RouteResponse{Status: "queued"},
			wantDBStatus:  "submitted",
			wantLegStatus: legSubmitted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, leg, reason := legOutcome(tt.resp, tt.err)
			if db != tt.wantDBStatus || leg != tt.wantLegStatus || reason != tt.wantReason {
				t.Errorf("got (%q, %q, %q), want (%q, %q, %q)",
					db, leg, reason, tt.wantDBStatus, tt.wantLegStatus, tt.wantReason)
			}
		})
	}
}

func TestDecodeRouteResponse(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantSteps  int
		wantStatus string
		unplaced   bool
	}{
		{
			name:       "steps absent is normal, not an error",
			body:       `{"execution_id":"e1","tx_hash":"0xabc","status":"confirmed"}`,
			wantStatus: "confirmed",
		},
		{
			name:       "steps null decodes to nil",
			body:       `{"execution_id":"e1","status":"failed","steps":null}`,
			wantStatus: "failed",
		},
		{
			name:       "steps empty array",
			body:       `{"execution_id":"e1","status":"failed","steps":[]}`,
			wantStatus: "failed",
		},
		{
			name:       "rebalance reverted after the withdraw landed",
			body:       `{"execution_id":"e1","tx_hash":"0xbbb","status":"failed","steps":[{"step":0,"tx_hash":"0xaaa","outcome":"confirmed"},{"step":1,"tx_hash":"0xbbb","outcome":"reverted"}]}`,
			wantSteps:  2,
			wantStatus: "failed",
			unplaced:   true,
		},
		{
			name:       "reverted on the first step leaves funds where they were",
			body:       `{"execution_id":"e1","status":"failed","steps":[{"step":0,"tx_hash":"0xaaa","outcome":"reverted"}]}`,
			wantSteps:  1,
			wantStatus: "failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp executor.RouteResponse
			if err := json.Unmarshal([]byte(tt.body), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", resp.Status, tt.wantStatus)
			}
			if len(resp.Steps) != tt.wantSteps {
				t.Errorf("got %d steps, want %d", len(resp.Steps), tt.wantSteps)
			}
			if got := fundsUnplaced(resp); got != tt.unplaced {
				t.Errorf("fundsUnplaced = %v, want %v", got, tt.unplaced)
			}
		})
	}
}

func TestSettledStatus(t *testing.T) {
	tests := []struct {
		name                string
		failed, pendingLegs int
		want                int
	}{
		{"all settled", 0, 0, http.StatusOK},
		{"a failed leg", 1, 0, http.StatusMultiStatus},
		{"a pending leg is unsettled too", 0, 1, http.StatusMultiStatus},
		{"both", 2, 3, http.StatusMultiStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := settledStatus(tt.failed, tt.pendingLegs); got != tt.want {
				t.Errorf("settledStatus(%d, %d) = %d, want %d", tt.failed, tt.pendingLegs, got, tt.want)
			}
		})
	}
}

// --- valuation must never invent a number ---

// stubRPC returns a fixed eth_call result, or an error for every call.
type stubRPC struct {
	decimals, round string
	err             error
}

func (s stubRPC) Call(_ context.Context, _, data string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if data == "0x313ce567" {
		return s.decimals, nil
	}
	return s.round, nil
}

// abiWord left-pads a value to a 32-byte hex word.
func abiWord(v int64) string {
	return fmt.Sprintf("%064x", big.NewInt(v))
}

func roundAt(answer int64, ts time.Time) string {
	return "0x" + abiWord(1) + abiWord(answer) + abiWord(ts.Unix()) + abiWord(ts.Unix()) + abiWord(1)
}

func TestReconcileNeverFabricatesAValue(t *testing.T) {
	fresh := time.Now().Add(-time.Minute)
	usdcBalance := []tokenapi.Balance{{Symbol: "USDC", Value: 600, Decimals: 6}}

	tests := []struct {
		name       string
		position   store.Position
		balances   []tokenapi.Balance
		rpc        prices.RPC
		wantUSD    *float64
		wantOK     bool
		wantReason string
	}{
		{
			name:     "priced and agreeing",
			position: store.Position{Asset: "USDC", AmountUSD: 600},
			balances: usdcBalance,
			rpc:      stubRPC{decimals: "0x" + abiWord(8), round: roundAt(99990801, fresh)},
			wantUSD:  ptr(599.944806),
			wantOK:   true,
		},
		{
			name:     "priced but disagreeing is reported, not hidden",
			position: store.Position{Asset: "USDC", AmountUSD: 1000},
			balances: usdcBalance,
			rpc:      stubRPC{decimals: "0x" + abiWord(8), round: roundAt(100000000, fresh)},
			wantUSD:  ptr(600),
			wantOK:   false,
		},
		{
			name:       "no feed yields null, never a dollar",
			position:   store.Position{Asset: "PEPE", AmountUSD: 600},
			balances:   []tokenapi.Balance{{Symbol: "PEPE", Value: 600}},
			rpc:        stubRPC{decimals: "0x" + abiWord(8), round: roundAt(100000000, fresh)},
			wantReason: "no Chainlink price feed for PEPE on Base; value unknown",
		},
		{
			name:     "stale feed yields null, never a dollar",
			position: store.Position{Asset: "USDC", AmountUSD: 600},
			balances: usdcBalance,
			// USDC's feed has a 24h heartbeat, so "stale" means well past that.
			rpc:        stubRPC{decimals: "0x" + abiWord(8), round: roundAt(100000000, time.Now().Add(-40*time.Hour))},
			wantReason: "the USDC price feed is stale; value unknown",
		},
		{
			name:       "unreachable rpc yields null, never a dollar",
			position:   store.Position{Asset: "USDC", AmountUSD: 600},
			balances:   usdcBalance,
			rpc:        stubRPC{err: errors.New("over rate limit")},
			wantReason: "could not read the USDC price feed; value unknown",
		},
		{
			name:       "receipt token is not silently treated as the underlying",
			position:   store.Position{Asset: "USDC", AmountUSD: 600},
			balances:   []tokenapi.Balance{{Symbol: "mUSDC", Value: 600}},
			rpc:        stubRPC{decimals: "0x" + abiWord(8), round: roundAt(100000000, fresh)},
			wantReason: "no matching onchain balance; the position is likely held as a venue receipt token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mainnet explicitly: these fixtures are the mainnet USDC feed,
			// including its 24h heartbeat.
			s := &Server{Prices: prices.New(tt.rpc, prices.ChainBaseMainnet, time.Hour, time.Minute)}
			got, ok, reason := s.reconcile(context.Background(), tt.position, tt.balances)
			if tt.wantUSD == nil && got != nil {
				t.Fatalf("onchain_usd = %v, want null", *got)
			}
			if tt.wantUSD != nil {
				if got == nil {
					t.Fatalf("onchain_usd = null, want %v", *tt.wantUSD)
				}
				if math.Abs(*got-*tt.wantUSD) > 1e-6 {
					t.Errorf("onchain_usd = %v, want %v", *got, *tt.wantUSD)
				}
			}
			if ok != tt.wantOK {
				t.Errorf("reconciled = %v, want %v", ok, tt.wantOK)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

func ptr(f float64) *float64 { return &f }
