package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
	"github.com/snehendu098/sweem-basket/internal/shared/prices"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/executor"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/marketdata"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/store"
)

// Deposits arrive in USDC. Anything else has to be bought first, so a leg with
// no allowlisted path cannot be funded however good its venue rate is.
func TestSwapFundableFollowsTheExecutorsPaths(t *testing.T) {
	s := fundingServer(t, nil, nil, []executor.SwapPath{
		{ChainID: 8453, From: "USDC", To: "WETH", Hops: 1},
	})

	tests := []struct {
		name, asset, chain string
		want               error
	}{
		{"quote asset needs no swap", "USDC", chains.LabelBaseMainnet, nil},
		{"quote asset needs no swap even where nothing is swappable", "USDC", chains.LabelBaseSepolia, nil},
		{"allowlisted path", "WETH", chains.LabelBaseMainnet, nil},
		{"no path on this chain", "WETH", chains.LabelBaseSepolia, ErrNoSwapPath},
		{"unlisted asset", "DOGE", chains.LabelBaseMainnet, ErrNoSwapPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.swapFundable(context.Background(), tt.asset, tt.chain)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// Same rule the venue allowlist follows: if we cannot read it we do not route.
func TestSwapFundableFailsWhenThePathsCannotBeRead(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(down.Close)
	s := &Server{Executor: executor.New(down.URL)}

	if err := s.swapFundable(context.Background(), "WETH", chains.LabelBaseMainnet); err == nil {
		t.Fatal("routed a swap leg without knowing which paths exist")
	}
	if err := s.swapFundable(context.Background(), "USDC", chains.LabelBaseMainnet); err != nil {
		// A USDC leg never touches Uniswap, so an unreachable executor must not
		// take it down with the rest.
		t.Fatalf("quote-asset leg must not depend on the swap allowlist: %v", err)
	}
}

// The plan is the contract with the user: an unfundable leg is named there,
// before they commit, rather than failing at submission.
func TestPlanLegsMarksUnfundableLegs(t *testing.T) {
	s := fundingServer(t,
		[]marketdata.Venue{{ID: "sep:aave-v3:0xbb", Project: "aave-v3", APY: 4.2, Chain: chains.LabelBaseSepolia}},
		[]executor.AllowedVenue{{ID: "sep:aave-v3:0xbb", ChainID: 84532}},
		// Base Sepolia has no Uniswap paths at all; mainnet's are irrelevant here.
		[]executor.SwapPath{{ChainID: 8453, From: "USDC", To: "WETH", Hops: 1}},
	)
	fresh := time.Now()
	s.Prices = prices.Set{prices.ChainBaseSepolia: prices.New(
		stubRPC{decimals: "0x" + abiWord(8), round: roundAt(100000000, fresh)},
		prices.ChainBaseSepolia, time.Hour, time.Minute)}

	legs, _ := s.planLegs(
		httptest.NewRequest(http.MethodGet, "/", nil),
		store.Basket{Chain: chains.LabelBaseSepolia, Weights: []store.Weight{
			{Asset: "USDC", WeightBps: 5000},
			{Asset: "WETH", WeightBps: 5000},
		}}, 100)

	if len(legs) != 2 {
		t.Fatalf("got %d legs, want 2", len(legs))
	}
	usdc, weth := legs[0], legs[1]
	if usdc.Venue == nil {
		t.Fatalf("the quote asset needs no swap and must stay routable: %q", usdc.Reason)
	}
	if weth.Venue != nil {
		t.Fatal("proposed a venue for an asset the executor cannot buy")
	}
	for _, want := range []string{"USDC", "WETH", chains.LabelBaseSepolia, "cannot be funded"} {
		if !strings.Contains(weth.Reason, want) {
			t.Fatalf("reason %q must name %q", weth.Reason, want)
		}
	}
}

// What the client consumes instead of its hardcoded per-chain boolean.
func TestPublicSwapPathsServesTheExecutorsList(t *testing.T) {
	s := fundingServer(t, nil, nil, []executor.SwapPath{
		{ChainID: 8453, From: "USDC", To: "WETH", Hops: 1},
		{ChainID: 8453, From: "USDC", To: "wstETH", Hops: 2},
	})
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public/swap-paths", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got struct {
		QuoteAsset string              `json:"quote_asset"`
		Count      int                 `json:"count"`
		Paths      []executor.SwapPath `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.QuoteAsset != QuoteAsset || got.Count != 2 || got.Paths[1].To != "wstETH" || got.Paths[1].Hops != 2 {
		t.Fatalf("got %+v", got)
	}

	// An unreachable executor is 503, not an empty list: "nothing is swappable"
	// and "we could not ask" must not look the same to the client.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(down.Close)
	s.Executor = executor.New(down.URL)
	rec = httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public/swap-paths", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}
