// Package api holds the wallet service HTTP surface.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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
	// Keeper authenticates the keeper service on the rebalance route only.
	// An empty secret disables it.
	Keeper auth.KeeperAuth
	// TokenAPI may be nil when TOKEN_API_JWT is unset; the portfolio endpoint
	// then serves the DB view alone rather than failing.
	TokenAPI *tokenapi.Client
	// Prices reads Chainlink feeds. Nothing is valued without it.
	Prices *prices.Client
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

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.health)

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
