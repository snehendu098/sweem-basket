// Command publisher polls yield sources on an interval and republishes the
// normalized venue set into Redis.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/config"
	"github.com/snehendu098/sweem-basket/internal/shared/prices"
	"github.com/snehendu098/sweem-basket/internal/shared/redisclient"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/source"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/store"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

func main() {
	config.LoadDotEnv(config.GetEnv("ENV_FILE", ".env"))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.Level(config.GetEnvInt("LOG_LEVEL", int(slog.LevelInfo))),
	})))

	interval := config.GetEnvDuration("POLL_INTERVAL", 5*time.Minute)
	filter := source.Filter{
		Chains: config.GetEnvList("CHAINS", []string{"Base"}),
		// Testnet-scaled: Base Sepolia's whole Aave+Comet universe is six figures,
		// not eight. A mainnet-sized floor drops every venue but one, which reads
		// as a broken router rather than as a threshold doing its job.
		MinTVLUsd: config.GetEnvFloat("MIN_TVL_USD", 5_000),
		MaxAPY:    config.GetEnvFloat("MAX_APY", 100),
	}

	// USD prices come from Chainlink on Base — the same aggregators the lending
	// venues price against. Subgraphs that report token units (Morpho) and
	// reserves whose own oracle returns 0 (Aave) are valued from here, or dropped.
	feed := prices.New(
		prices.NewHTTPRPC(config.GetEnv("BASE_RPC_URL", "https://sepolia.base.org")),
		// CHAIN_ID picks the whole Chainlink feed table. On Base Sepolia only USDC
		// and ETH/WETH have feeds; anything else is dropped with a logged reason.
		config.GetEnvInt("CHAIN_ID", prices.DefaultChainID),
		config.GetEnvDuration("PRICE_MAX_AGE", time.Hour), // grace atop each feed's own heartbeat
		config.GetEnvDuration("PRICE_CACHE_TTL", time.Minute),
	)

	chain := filter.Chains[0] // subgraph deployments are per-chain; primary chain drives the adapter set
	src, err := source.NewGraph(
		config.GetEnv("GRAPH_GATEWAY_URL", source.DefaultGatewayURL),
		os.Getenv("GRAPH_API_KEY"),
		filter,
		feed,
		source.Adapters(chain)...,
	)
	if err != nil {
		slog.Error("graph source unavailable", "err", err)
		os.Exit(1)
	}

	rdb, err := redisclient.New()
	if err != nil {
		slog.Error("redis config", "err", err)
		os.Exit(1)
	}
	defer rdb.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Error("redis unreachable", "err", err)
		os.Exit(1)
	}

	st := store.New(rdb)
	sources := []source.Source{src}
	ttl := 3 * interval

	slog.Info("publisher starting", "interval", interval, "chains", filter.Chains, "adapters", len(src.Adapters),
		"min_tvl_usd", filter.MinTVLUsd, "max_apy", filter.MaxAPY, "ttl", ttl)

	runCycle(ctx, st, sources, ttl)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("publisher stopped")
			return
		case <-ticker.C:
			runCycle(ctx, st, sources, ttl)
		}
	}
}

// runCycle never fails the process: a source outage leaves the last-good Redis
// state in place until its TTL expires.
func runCycle(ctx context.Context, st *store.Store, sources []source.Source, ttl time.Duration) {
	started := time.Now()
	seen := map[string]venue.Venue{}
	var statuses []source.Status
	ok := false
	for _, src := range sources {
		fetchCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		venues, err := src.Fetch(fetchCtx)
		cancel()
		if reporter, hasStatus := src.(interface{ Status() []source.Status }); hasStatus {
			statuses = append(statuses, reporter.Status()...)
		}
		if err != nil {
			slog.Error("source fetch failed, keeping last-good state", "source", src.Name(), "err", err)
			continue
		}
		ok = true
		slog.Info("source fetched", "source", src.Name(), "venues", len(venues))
		for _, v := range venues {
			// Same venue from two sources: keep the higher reported APY.
			if prev, dup := seen[v.ID]; dup && prev.APY >= v.APY {
				continue
			}
			seen[v.ID] = v
		}
	}
	// Adapter health is recorded even when the cycle produced nothing usable.
	if err := st.PublishSources(ctx, statuses, ttl); err != nil {
		slog.Warn("publish source status failed", "err", err)
	}
	if !ok {
		return
	}

	merged := make([]venue.Venue, 0, len(seen))
	for _, v := range seen {
		merged = append(merged, v)
	}
	if err := st.Publish(ctx, merged, ttl); err != nil {
		slog.Error("publish failed", "err", err)
		return
	}
	slog.Info("cycle complete", "venues", len(merged), "took", time.Since(started).String())
}
