// Package api holds the wallet service HTTP surface.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
	"github.com/snehendu098/sweem-basket/internal/shared/prices"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/auth"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/executor"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/marketdata"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/store"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/tokenapi"
)

type Server struct {
	Store    *store.Store
	Market   *marketdata.Client
	Executor *executor.Client
	Auth     *auth.Verifier
	// PublicStore backs the unauthenticated /public routes. Nil means Store,
	// which is what production uses; it is a seam so those routes can be
	// tested without a database.
	PublicStore basketReader
	// Keeper authenticates the keeper service on the rebalance route only.
	// An empty secret disables it.
	Keeper auth.KeeperAuth
	// TokenAPI may be nil when TOKEN_API_JWT is unset; the portfolio endpoint
	// then serves the DB view alone rather than failing.
	TokenAPI *tokenapi.Client
	// Prices reads Chainlink feeds, one client per chain. A basket names its
	// chain, and that chain picks the feed table: there is no global price
	// client to accidentally value a mainnet position off Sepolia's two feeds.
	Prices prices.Set
	Log    *slog.Logger
	// MinVenueTVL is the liquidity floor for routing. A venue thinner than this
	// cannot absorb a deposit without moving the rate against us.
	MinVenueTVL float64
	// DefaultChain is used when a request does not name one.
	DefaultChain string
	// RebalanceThresholdAPY is the drift, in percentage points, below which
	// moving costs more in gas and slippage than the extra yield is worth.
	RebalanceThresholdAPY float64
	// MaxSlippageBps is passed through to the executor on every route.
	MaxSlippageBps int
}

// chainOf resolves a stored chain label to the id every chain-aware dependency
// keys on. An unrecognised label is an error, never a default: a basket whose
// chain we cannot name is a basket we must not move money for.
func chainOf(label string) (int, error) {
	id, ok := chains.ID(label)
	if !ok {
		return 0, fmt.Errorf("unknown chain %q", label)
	}
	return id, nil
}

// priceUSD prices an asset on the chain that actually holds it.
func (s *Server) priceUSD(ctx context.Context, chain, asset string) (prices.Price, error) {
	id, err := chainOf(chain)
	if err != nil {
		return prices.Price{}, err
	}
	return s.Prices.USD(ctx, id, asset)
}

// ErrNoRoutableVenue means market-data knows venues for this asset but the
// executor cannot transact with any of them.
var ErrNoRoutableVenue = errors.New("no executable venue")

// bestRoutable picks the highest-APY venue the executor will actually accept.
//
// market-data indexes every venue it can see, including protocols this stack
// cannot encode calldata for (Moonwell's mTokens, for one). Proposing one of
// those is a route that fails at submission time, after an execution row exists
// — so the allowlist, not the rate table, decides what is routable. When the
// best indexed venue is not executable the caller gets the best one that is,
// plus a note naming what was passed over: a visible downgrade beats a silent
// one, and beats pretending the better rate does not exist.
func (s *Server) bestRoutable(ctx context.Context, asset, chain string) (marketdata.Venue, string, error) {
	allow, err := s.Executor.Allowlist(ctx)
	if err != nil {
		// No allowlist, no routing. Guessing here is exactly the bug this
		// function exists to remove.
		return marketdata.Venue{}, "", fmt.Errorf("executor allowlist unavailable: %w", err)
	}
	venues, err := s.Market.Venues(ctx, asset, chain, s.MinVenueTVL, 20)
	if err != nil {
		return marketdata.Venue{}, "", err
	}
	if len(venues) == 0 {
		return marketdata.Venue{}, "", marketdata.ErrNoVenue
	}

	var passed []marketdata.Venue // better rates we cannot reach
	for _, v := range venues {
		if _, ok := allow[v.ID]; !ok {
			passed = append(passed, v)
			continue
		}
		return v, downgradeNote(passed, v), nil
	}
	return marketdata.Venue{}, "", fmt.Errorf("%w for %s on %s", ErrNoRoutableVenue, asset, chain)
}

// QuoteAsset is what every deposit is denominated in. A leg in any other asset
// has to be bought on the way in, so it depends on a swap path existing.
const QuoteAsset = "USDC"

// ErrNoSwapPath means the executor has no allowlisted way to buy this asset
// with the quote asset on this chain, so the leg cannot be funded at all.
var ErrNoSwapPath = errors.New("no swap path")

// swapFundable reports whether a leg's asset can actually be acquired.
//
// The venue allowlist answers "can the executor supply this?"; it says nothing
// about "can the executor get hold of the token in the first place?". Deposits
// arrive in USDC, and anything else is bought on Uniswap v3 against a path in
// the executor's swaps.json. No path, no leg — however good the venue rate is.
// A fetch failure fails the leg too: guessing here is the bug this removes.
func (s *Server) swapFundable(ctx context.Context, asset, chain string) error {
	if asset == QuoteAsset {
		return nil // already holding what the user deposited
	}
	id, err := chainOf(chain)
	if err != nil {
		return err
	}
	paths, err := s.Executor.SwapPaths(ctx)
	if err != nil {
		return fmt.Errorf("executor swap paths unavailable: %w", err)
	}
	if !executor.HasSwapPath(paths, id, QuoteAsset, asset) {
		return fmt.Errorf("%w %s->%s on %s", ErrNoSwapPath, QuoteAsset, asset, chain)
	}
	return nil
}

// downgradeNote explains, in the plan, which better venue was skipped and why.
func downgradeNote(passed []marketdata.Venue, chosen marketdata.Venue) string {
	if len(passed) == 0 {
		return ""
	}
	best := passed[0]
	return fmt.Sprintf(
		"best rate is %.2f%% at %s, which this executor cannot transact; routing to %s at %.2f%% instead",
		best.APY, best.Project, chosen.Project, chosen.APY)
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.health)

	// Discovery is browsable without a wallet: a basket marked is_public is by
	// definition meant to be found. These are outside the /v1/ authed mux and
	// serve a narrowed view (no creator_id, no subscribed) — see public.go.
	mux.HandleFunc("GET /public/baskets", s.publicListBaskets)
	mux.HandleFunc("GET /public/baskets/{id}", s.publicGetBasket)

	// The executor's swap allowlist, proxied. Static config, no caller in it.
	mux.HandleFunc("GET /public/swap-paths", s.publicSwapPaths)

	// Everything below requires a valid Privy access token.
	authed := http.NewServeMux()
	authed.HandleFunc("GET /v1/me", s.getMe)
	authed.HandleFunc("POST /v1/me", s.upsertMe)
	authed.HandleFunc("POST /v1/baskets", s.createBasket)
	authed.HandleFunc("GET /v1/baskets", s.listBaskets)
	authed.HandleFunc("GET /v1/baskets/{id}", s.getBasket)
	authed.HandleFunc("POST /v1/baskets/{id}/subscribe", s.subscribe)
	authed.HandleFunc("DELETE /v1/baskets/{id}/subscribe", s.unsubscribe)
	authed.HandleFunc("GET /v1/baskets/{id}/plan", s.planBasket)
	authed.HandleFunc("POST /v1/baskets/{id}/deposit", s.deposit)
	authed.HandleFunc("POST /v1/baskets/{id}/withdraw", s.withdraw)
	authed.HandleFunc("GET /v1/portfolio", s.portfolio)
	authed.HandleFunc("GET /v1/executions", s.executions)

	// Rebalance is the one route the keeper may reach, and it is scoped by
	// registration rather than by a check inside a shared middleware: this
	// pattern is more specific than "/v1/", so it wins, and the keeper path
	// physically cannot be reached on any other route.
	mux.Handle("POST /v1/baskets/{id}/rebalance",
		s.Auth.MiddlewareAllowingKeeper(s.Keeper, http.HandlerFunc(s.rebalance)))

	mux.Handle("/v1/", s.Auth.Middleware(authed))
	return logging(s.Log, mux)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// caller resolves the authenticated Privy DID to a local user row.
func (s *Server) caller(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	c, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return store.User{}, false
	}
	u, err := s.Store.UserByDID(r.Context(), c.DID)
	if errors.Is(err, store.ErrNotFound) {
		// The client must POST /v1/me once to bind a wallet address to the DID.
		writeErr(w, http.StatusPreconditionRequired, "user not registered; POST /v1/me first")
		return store.User{}, false
	}
	if err != nil {
		s.Log.Error("lookup user", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return store.User{}, false
	}
	return u, true
}

func logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Debug("request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
