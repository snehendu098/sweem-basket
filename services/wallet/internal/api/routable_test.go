package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snehendu098/sweem-basket/services/wallet/internal/executor"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/marketdata"
)

// stubs the two services routing depends on: market-data's rate table and the
// executor's two allowlists (venues and swap paths).
func routingServer(t *testing.T, venues []marketdata.Venue, allowed []executor.AllowedVenue) *Server {
	t.Helper()
	// The shipped mainnet paths, so venue tests are not accidentally about
	// funding. Tests that care pass their own via fundingServer.
	return fundingServer(t, venues, allowed, []executor.SwapPath{
		{ChainID: 8453, From: "USDC", To: "WETH", Hops: 1},
		{ChainID: 8453, From: "WETH", To: "USDC", Hops: 1},
	})
}

func fundingServer(t *testing.T, venues []marketdata.Venue, allowed []executor.AllowedVenue, paths []executor.SwapPath) *Server {
	t.Helper()
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"venues": venues},
		})
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

// The router must never propose a venue the executor would refuse. The best
// rate being unreachable is reported, not silently dropped and not promised.
func TestBestRoutableSkipsUnexecutableVenuesAndSaysSo(t *testing.T) {
	s := routingServer(t,
		[]marketdata.Venue{
			{ID: "base:moonwell:0xaa", Project: "moonwell", Asset: "USDC", APY: 14.5},
			{ID: "base:aave-v3:0xbb", Project: "aave-v3", Asset: "USDC", APY: 4.2},
		},
		[]executor.AllowedVenue{{ID: "base:aave-v3:0xbb", ChainID: 8453, Symbol: "USDC"}},
	)

	v, note, err := s.bestRoutable(context.Background(), "USDC", "base")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if v.ID != "base:aave-v3:0xbb" {
		t.Fatalf("routed to %s, want the allowlisted venue", v.ID)
	}
	if note == "" {
		t.Fatal("a downgrade must be explained, not silent")
	}
	for _, want := range []string{"moonwell", "14.5"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note %q must name the better venue and its rate", note)
		}
	}
}

// Nothing executable is its own answer, distinct from "no venue exists".
func TestBestRoutableReportsWhenNothingIsExecutable(t *testing.T) {
	s := routingServer(t,
		[]marketdata.Venue{{ID: "base:moonwell:0xaa", Project: "moonwell", APY: 14.5}},
		[]executor.AllowedVenue{{ID: "base:aave-v3:0xbb", ChainID: 8453}},
	)
	if _, _, err := s.bestRoutable(context.Background(), "USDC", "base"); !errors.Is(err, ErrNoRoutableVenue) {
		t.Fatalf("err = %v, want ErrNoRoutableVenue", err)
	}
}

// No allowlist means no routing: guessing is the bug this exists to remove.
func TestBestRoutableFailsWhenTheAllowlistCannotBeRead(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(down.Close)
	s := routingServer(t, []marketdata.Venue{{ID: "base:aave-v3:0xbb", APY: 4}}, nil)
	s.Executor = executor.New(down.URL)

	if _, _, err := s.bestRoutable(context.Background(), "USDC", "base"); err == nil {
		t.Fatal("routed without knowing what is allowlisted")
	}
}

// The allowlist changes only on executor restart, so it is fetched once.
func TestAllowlistIsCached(t *testing.T) {
	calls := 0
	ex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"venues": []executor.AllowedVenue{{ID: "base:aave-v3:0xbb", ChainID: 8453}},
		})
	}))
	t.Cleanup(ex.Close)
	c := executor.New(ex.URL)
	for i := 0; i < 3; i++ {
		if _, err := c.Allowlist(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("fetched the allowlist %d times, want 1", calls)
	}
}
