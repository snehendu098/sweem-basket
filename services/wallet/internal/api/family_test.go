package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/snehendu098/sweem-basket/services/wallet/internal/executor"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/marketdata"
)

func familyServer(
	t *testing.T,
	assets []marketdata.Asset,
	venues []marketdata.Venue,
	allowed []executor.AllowedVenue,
	paths []executor.SwapPath,
) *Server {
	t.Helper()
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/assets" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"assets": assets},
			})
			return
		}
		asset := r.URL.Query().Get("asset")
		out := make([]marketdata.Venue, 0, len(venues))
		for _, v := range venues {
			if asset == "" || v.Asset == asset {
				out = append(out, v)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"venues": out}})
	}))
	t.Cleanup(md.Close)

	ex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/swaps" {
			_ = json.NewEncoder(w).Encode(map[string]any{"paths": paths, "count": len(paths)})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"venues": allowed, "count": len(allowed)})
	}))
	t.Cleanup(ex.Close)

	return &Server{
		Market:   marketdata.New(md.URL),
		Executor: executor.New(ex.URL),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func ethFamily(t *testing.T, allowed []executor.AllowedVenue, paths []executor.SwapPath) *Server {
	t.Helper()
	return familyServer(t,
		[]marketdata.Asset{
			{Asset: "wstETH", Chain: "base", Family: "ETH", Routable: 1, BestAPY: 4.9},
			{Asset: "WETH", Chain: "base", Family: "ETH", Routable: 1, BestAPY: 1.7},
			{Asset: "USDC", Chain: "base", Family: "USD", Routable: 1, BestAPY: 5.7},
		},
		[]marketdata.Venue{
			{ID: "base:aave-v3:0xwsteth", Asset: "wstETH", Chain: "base", APY: 4.9},
			{ID: "base:aave-v3:0xweth", Asset: "WETH", Chain: "base", APY: 1.7},
			{ID: "base:aave-v3:0xusdc", Asset: "USDC", Chain: "base", APY: 5.7},
		},
		allowed, paths)
}

var allETHPaths = []executor.SwapPath{
	{ChainID: 8453, From: "USDC", To: "wstETH", Hops: 2},
	{ChainID: 8453, From: "USDC", To: "WETH", Hops: 1},
}

var allETHVenues = []executor.AllowedVenue{
	{ID: "base:aave-v3:0xwsteth", ChainID: 8453, Symbol: "wstETH"},
	{ID: "base:aave-v3:0xweth", ChainID: 8453, Symbol: "WETH"},
}

func TestResolveFamilyTakesTheBestInstrument(t *testing.T) {
	s := ethFamily(t, allETHVenues, allETHPaths)
	got, err := s.resolveFamily(context.Background(), "ETH", "base")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "wstETH" {
		t.Fatalf("resolved to %q, want wstETH at 4.9%%", got)
	}
}

// An instrument the executor cannot fund is not a candidate, however good the
// rate: the family must fall through to one that can actually be bought.
func TestResolveFamilySkipsAnInstrumentWithNoSwapPath(t *testing.T) {
	s := ethFamily(t, allETHVenues, []executor.SwapPath{
		{ChainID: 8453, From: "USDC", To: "WETH", Hops: 1},
	})
	got, err := s.resolveFamily(context.Background(), "ETH", "base")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "WETH" {
		t.Fatalf("resolved to %q, want WETH: wstETH cannot be funded", got)
	}
}

func TestResolveFamilySkipsAnInstrumentWithNoRoutableVenue(t *testing.T) {
	s := ethFamily(t, []executor.AllowedVenue{
		{ID: "base:aave-v3:0xweth", ChainID: 8453, Symbol: "WETH"},
	}, allETHPaths)
	got, err := s.resolveFamily(context.Background(), "ETH", "base")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "WETH" {
		t.Fatalf("resolved to %q, want WETH: the wstETH venue is not on the allowlist", got)
	}
}

func TestResolveFamilyReportsWhenNoInstrumentWorks(t *testing.T) {
	s := ethFamily(t, []executor.AllowedVenue{
		{ID: "base:aave-v3:0xusdc", ChainID: 8453, Symbol: "USDC"},
	}, allETHPaths)
	if _, err := s.resolveFamily(context.Background(), "ETH", "base"); !errors.Is(err, ErrNoFamilyInstrument) {
		t.Fatalf("err = %v, want ErrNoFamilyInstrument", err)
	}
}

// Unknown is not empty: an unreadable allowlist must not read as "this family
// has no instruments", which would silently idle the leg.
func TestResolveFamilyFailsWhenTheExecutorCannotBeRead(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)

	s := ethFamily(t, allETHVenues, allETHPaths)
	s.Executor = executor.New(down.URL)

	_, err := s.resolveFamily(context.Background(), "ETH", "base")
	if err == nil || errors.Is(err, ErrNoFamilyInstrument) {
		t.Fatalf("err = %v, want the transport failure surfaced", err)
	}
}
