package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

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

	var passed []unreachable
	for _, v := range venues {
		if _, ok := allow[v.ID]; !ok {
			passed = append(passed, unreachable{v, "this executor cannot transact"})
			continue
		}
		if v.NotRoutable != "" {
			passed = append(passed, unreachable{v, v.NotRoutable})
			continue
		}
		return v, downgradeNote(passed, v), nil
	}
	return marketdata.Venue{}, "", fmt.Errorf("%w for %s on %s", ErrNoRoutableVenue, asset, chain)
}

// A pin is a preference, not an override of safety: it must clear every gate
// bestRoutable clears, and failing one fails the leg rather than substituting.
func (s *Server) pinnedRoutable(ctx context.Context, venueID, asset, chain string) (marketdata.Venue, error) {
	allow, err := s.Executor.Allowlist(ctx)
	if err != nil {
		return marketdata.Venue{}, fmt.Errorf("executor allowlist unavailable: %w", err)
	}
	venues, err := s.Market.Venues(ctx, asset, chain, s.MinVenueTVL, 100)
	if err != nil {
		return marketdata.Venue{}, err
	}
	for _, v := range venues {
		if v.ID != venueID {
			continue
		}
		switch {
		case !strings.EqualFold(v.Chain, chain):
			return marketdata.Venue{}, fmt.Errorf("%w: pinned venue %s is on %s, not %s", ErrPinUnusable, venueID, v.Chain, chain)
		case v.NotRoutable != "":
			return marketdata.Venue{}, fmt.Errorf("%w: pinned venue %s is not routable: %s", ErrPinUnusable, venueID, v.NotRoutable)
		}
		if _, ok := allow[venueID]; !ok {
			return marketdata.Venue{}, fmt.Errorf("%w: pinned venue %s is not on the executor allowlist", ErrPinUnusable, venueID)
		}
		return v, nil
	}
	return marketdata.Venue{}, fmt.Errorf("%w: pinned venue %s is not published for %s on %s", ErrPinUnusable, venueID, asset, chain)
}

var ErrNoFamilyInstrument = errors.New("no routable instrument in family")

// The family names the exposure; the instrument is chosen here, at deposit, so
// each depositor gets the best one as of their own deposit rather than the
// creator's snapshot from whenever the basket was published.
func (s *Server) resolveFamily(ctx context.Context, family, chain string) (string, error) {
	assets, err := s.Market.Assets(ctx, chain)
	if err != nil {
		return "", fmt.Errorf("market data unavailable: %w", err)
	}
	for _, a := range assets {
		if !strings.EqualFold(a.Family, family) || a.Routable == 0 {
			continue
		}
		if err := s.swapFundable(ctx, a.Asset, chain); err != nil {
			if errors.Is(err, ErrNoSwapPath) {
				continue
			}
			return "", err
		}
		switch _, _, err := s.bestRoutable(ctx, a.Asset, chain); {
		case errors.Is(err, marketdata.ErrNoVenue), errors.Is(err, ErrNoRoutableVenue):
			continue
		case err != nil:
			return "", err
		}
		return a.Asset, nil
	}
	return "", fmt.Errorf("%w: %s on %s", ErrNoFamilyInstrument, family, chain)
}

var ErrPinUnusable = errors.New("pinned venue unusable")

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

type unreachable struct {
	venue marketdata.Venue
	why   string
}

func downgradeNote(passed []unreachable, chosen marketdata.Venue) string {
	if len(passed) == 0 {
		return ""
	}
	best := passed[0]
	return fmt.Sprintf(
		"best rate is %.2f%% at %s, which %s; routing to %s at %.2f%% instead",
		best.venue.APY, best.venue.Project, best.why, chosen.Project, chosen.APY)
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
