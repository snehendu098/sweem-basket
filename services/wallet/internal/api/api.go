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
	Store                 *store.Store
	Market                *marketdata.Client
	Executor              *executor.Client
	Auth                  *auth.Verifier
	PublicStore           basketReader
	Keeper                auth.KeeperAuth
	TokenAPI              *tokenapi.Client
	Prices                prices.Set
	Log                   *slog.Logger
	MinVenueTVL           float64
	DefaultChain          string
	RebalanceThresholdAPY float64
	MaxSlippageBps        int
}

func chainOf(label string) (int, error) {
	id, ok := chains.ID(label)
	if !ok {
		return 0, fmt.Errorf("unknown chain %q", label)
	}
	return id, nil
}

func (s *Server) priceUSD(ctx context.Context, chain, asset string) (prices.Price, error) {
	id, err := chainOf(chain)
	if err != nil {
		return prices.Price{}, err
	}
	return s.Prices.USD(ctx, id, asset)
}

var ErrNoRoutableVenue = errors.New("no executable venue")

func (s *Server) bestRoutable(ctx context.Context, asset, chain string) (marketdata.Venue, string, error) {
	allow, err := s.Executor.Allowlist(ctx)
	if err != nil {
		return marketdata.Venue{}, "", fmt.Errorf("executor allowlist unavailable: %w", err)
	}
	venues, err := s.Market.Venues(ctx, asset, chain, s.MinVenueTVL, 20)
	if err != nil {
		return marketdata.Venue{}, "", err
	}
	if len(venues) == 0 {
		return marketdata.Venue{}, "", marketdata.ErrNoVenue
	}

	var passed []marketdata.Venue
	for _, v := range venues {
		if _, ok := allow[v.ID]; !ok {
			passed = append(passed, v)
			continue
		}
		return v, downgradeNote(passed, v), nil
	}
	return marketdata.Venue{}, "", fmt.Errorf("%w for %s on %s", ErrNoRoutableVenue, asset, chain)
}

const QuoteAsset = "USDC"

var ErrNoSwapPath = errors.New("no swap path")

func (s *Server) swapFundable(ctx context.Context, asset, chain string) error {
	if asset == QuoteAsset {
		return nil
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

	mux.HandleFunc("GET /public/baskets", s.publicListBaskets)
	mux.HandleFunc("GET /public/baskets/{id}", s.publicGetBasket)

	mux.HandleFunc("GET /public/swap-paths", s.publicSwapPaths)

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

	mux.Handle("POST /v1/baskets/{id}/rebalance",
		s.Auth.MiddlewareAllowingKeeper(s.Keeper, http.HandlerFunc(s.rebalance)))

	mux.Handle("/v1/", s.Auth.Middleware(authed))
	return logging(s.Log, mux)
}

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

func (s *Server) caller(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	c, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return store.User{}, false
	}
	u, err := s.Store.UserByDID(r.Context(), c.DID)
	if errors.Is(err, store.ErrNotFound) {
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
