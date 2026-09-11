// Command api serves the read-side HTTP API over the venue data in Redis.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/snehendu098/sweem-basket/internal/shared/config"
	"github.com/snehendu098/sweem-basket/internal/shared/httpx"
	"github.com/snehendu098/sweem-basket/internal/shared/redisclient"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/store"
)

type server struct {
	rdb        *redis.Client
	store      *store.Store
	venueCount atomic.Int64 // kept warm by the venues:updated subscription
}

func main() {
	config.LoadDotEnv(config.GetEnv("ENV_FILE", ".env"))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.Level(config.GetEnvInt("LOG_LEVEL", int(slog.LevelInfo))),
	})))

	rdb, err := redisclient.New()
	if err != nil {
		slog.Error("redis config", "err", err)
		os.Exit(1)
	}
	defer rdb.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s := &server{rdb: rdb, store: store.New(rdb)}
	if n, err := s.store.Count(ctx); err == nil {
		s.venueCount.Store(int64(n))
	}
	go s.watchUpdates(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /venues", s.venues)
	mux.HandleFunc("GET /venues/best", s.bestVenue)
	mux.HandleFunc("GET /assets", s.assets)
	mux.HandleFunc("GET /sources", s.sources)

	srv := &http.Server{
		Addr:              config.GetEnv("MARKET_DATA_ADDR", ":8081"),
		Handler:           logging(mux),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		slog.Info("api listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
	slog.Info("api stopped")
}

// watchUpdates keeps a live Redis subscription so /health reports a fresh count.
func (s *server) watchUpdates(ctx context.Context) {
	sub := s.rdb.Subscribe(ctx, store.ChanUpdate)
	defer sub.Close()
	for msg := range sub.Channel() {
		var u store.UpdateMessage
		if err := json.Unmarshal([]byte(msg.Payload), &u); err != nil {
			slog.Warn("bad update payload", "payload", msg.Payload, "err", err)
			continue
		}
		s.venueCount.Store(int64(u.Count))
		slog.Info("venues updated", "count", u.Count, "at", u.At)
	}
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	status, code := "ok", http.StatusOK
	redisState := "up"
	if err := s.rdb.Ping(r.Context()).Err(); err != nil {
		status, redisState, code = "degraded", "down", http.StatusServiceUnavailable
	} else if n, err := s.store.Count(r.Context()); err == nil {
		s.venueCount.Store(int64(n))
	}
	httpx.JSON(w, code, map[string]any{
		"status": status,
		"redis":  redisState,
		"venues": s.venueCount.Load(),
	})
}

func (s *server) venues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := intParam(q.Get("limit"), 20)
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
		return
	}
	minTVL, err := floatParam(q.Get("min_tvl"), 0)
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, "invalid_min_tvl", "min_tvl must be a number")
		return
	}

	venues, err := s.store.List(r.Context(), store.Query{
		Chain: q.Get("chain"), Asset: q.Get("asset"), MinTVL: minTVL, Limit: limit,
	})
	if err != nil {
		slog.Error("list venues", "err", err)
		httpx.Fail(w, http.StatusBadGateway, "store_unavailable", "could not read venues from redis")
		return
	}
	httpx.OK(w, map[string]any{"venues": venues, "count": len(venues)})
}

func (s *server) bestVenue(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	asset := q.Get("asset")
	if asset == "" {
		httpx.Fail(w, http.StatusBadRequest, "missing_asset", "asset query parameter is required")
		return
	}
	minTVL, err := floatParam(q.Get("min_tvl"), 0)
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, "invalid_min_tvl", "min_tvl must be a number")
		return
	}

	venues, err := s.store.List(r.Context(), store.Query{
		Chain: q.Get("chain"), Asset: asset, MinTVL: minTVL, Limit: 1,
	})
	if err != nil {
		slog.Error("best venue", "err", err)
		httpx.Fail(w, http.StatusBadGateway, "store_unavailable", "could not read venues from redis")
		return
	}
	if len(venues) == 0 {
		httpx.Fail(w, http.StatusNotFound, "no_venue",
			"no venue matches asset="+asset+" chain="+q.Get("chain")+" min_tvl="+q.Get("min_tvl"))
		return
	}
	httpx.OK(w, venues[0])
}

func (s *server) assets(w http.ResponseWriter, r *http.Request) {
	assets, err := s.store.Assets(r.Context(), r.URL.Query().Get("chain"))
	if err != nil {
		slog.Error("list assets", "err", err)
		httpx.Fail(w, http.StatusBadGateway, "store_unavailable", "could not read venues from redis")
		return
	}
	httpx.OK(w, map[string]any{"assets": assets, "count": len(assets)})
}

// sources exposes the per-protocol fetch status the publisher records in Redis.
func (s *server) sources(w http.ResponseWriter, r *http.Request) {
	raw, err := s.rdb.Get(r.Context(), store.KeySources).Result()
	if errors.Is(err, redis.Nil) {
		httpx.OK(w, map[string]any{"sources": []any{}, "count": 0})
		return
	}
	if err != nil {
		slog.Error("read sources", "err", err)
		httpx.Fail(w, http.StatusBadGateway, "store_unavailable", "could not read source status from redis")
		return
	}
	var statuses []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &statuses); err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "bad_status_payload", "stored source status is not valid json")
		return
	}
	httpx.OK(w, map[string]any{"sources": statuses, "count": len(statuses)})
}

func intParam(raw string, def int) (int, error) {
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, errors.New("invalid int param")
	}
	return v, nil
}

func floatParam(raw string, def float64) (float64, error) {
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		return 0, errors.New("invalid float param")
	}
	return v, nil
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Debug("request", "method", r.Method, "path", r.URL.Path,
			"query", r.URL.RawQuery, "took", time.Since(start).String())
	})
}
