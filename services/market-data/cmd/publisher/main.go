// Command publisher polls yield sources on an interval and republishes the
// normalized venue set into Redis.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
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

	// Both chains are served at once: the product offers a network toggle, so
	// "which chain" is a property of a request, never of the process. CHAINS
	// names them by canonical label; an unknown label is fatal rather than
	// quietly dropped, because a typo would otherwise look like an empty chain.
	labels := config.GetEnvList("CHAINS", []string{chains.LabelBaseMainnet, chains.LabelBaseSepolia})
	var targets []source.Chain
	for _, label := range labels {
		id, ok := chains.ID(label)
		if !ok {
			slog.Error("unknown chain label in CHAINS", "label", label, "supported", chains.Supported())
			os.Exit(1)
		}
		canonical, _ := chains.Label(id)
		targets = append(targets, source.Chain{Label: canonical, ID: id})
	}

	var sources []source.Source
	for _, c := range targets {
		// One filter, one price client and one GraphSource per chain. Nothing is
		// shared across chains, so a venue cannot pick up another chain's label,
		// price table or subgraph — and the thresholds are the chain's own.
		filter := source.FilterFor(c)

		// USD prices come from Chainlink on that chain — the same aggregators the
		// lending venues price against. Subgraphs that report token units (Morpho)
		// and reserves whose own oracle returns 0 (Aave) are valued from here, or
		// dropped. On Base Sepolia only USDC and ETH/WETH have feeds.
		node := prices.NewHTTPRPC(rpcURL(c.ID))
		feed := prices.New(
			node,
			c.ID,
			config.GetEnvDuration("PRICE_MAX_AGE", time.Hour), // grace atop each feed's own heartbeat
			config.GetEnvDuration("PRICE_CACHE_TTL", time.Minute),
		)

		src, err := source.NewGraph(
			c,
			config.GetEnv("GRAPH_GATEWAY_URL", source.DefaultGatewayURL),
			os.Getenv("GRAPH_API_KEY"),
			filter,
			feed,
			source.Adapters(c)...,
		)
		if err != nil {
			slog.Error("graph source unavailable", "chain", c.Label, "err", err)
			os.Exit(1)
		}
		sources = append(sources, src)

		// Aave and Compound publish a spot rate the node will hand over in one
		// eth_call, so they are read live rather than waiting on a subgraph to
		// replay chain history. Morpho and the liquid-staking rates have no
		// rate field at all and stay on the subgraph. Both sources run; see
		// source.Reconcile for how a venue both produce is merged.
		live, err := source.NewRPC(c, node, filter, feed)
		if err != nil {
			// A chain with no verified Pool or Comet addresses is a
			// configuration gap, not a reason to stop serving the other chain.
			slog.Warn("direct rate source unavailable for this chain", "chain", c.Label, "err", err)
		} else {
			sources = append(sources, live)
		}

		slog.Info("chain configured", "chain", c.Label, "chain_id", c.ID,
			"adapters", len(src.Adapters), "rpc", rpcURL(c.ID), "live_rates", err == nil,
			"min_tvl_usd", filter.MinTVLUsd, "max_apy", filter.MaxAPY,
			"allow_zero_apy", filter.AllowZeroAPY)
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
	ttl := 3 * interval

	slog.Info("publisher starting", "interval", interval, "chains", labels,
		"sources", len(sources), "ttl", ttl)

	tolerance := config.GetEnvFloat("RATE_DISAGREEMENT_TOLERANCE", source.DefaultRateTolerance)
	// A source gets longer than a subgraph query needs, because the direct-RPC
	// source deliberately paces itself: a public Base node answers about five
	// eth_calls a second, so ~60 reads is a minute of wall clock. Point
	// BASE_RPC_URL_<chainid> at a private node and this drops to seconds.
	fetchTimeout := config.GetEnvDuration("SOURCE_FETCH_TIMEOUT", 2*time.Minute)

	runCycle(ctx, st, sources, ttl, tolerance, fetchTimeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("publisher stopped")
			return
		case <-ticker.C:
			runCycle(ctx, st, sources, ttl, tolerance, fetchTimeout)
		}
	}
}

// rpcURL is per chain. There is no shared BASE_RPC_URL fallback: one node
// cannot answer for two networks, and reading a Chainlink feed off the wrong
// one returns a plausible number for the wrong asset universe.
func rpcURL(chainID int) string {
	def := "https://mainnet.base.org"
	if chainID == chains.BaseSepolia {
		def = "https://sepolia.base.org"
	}
	return config.GetEnv(fmt.Sprintf("BASE_RPC_URL_%d", chainID), def)
}

// runCycle never fails the process: a source outage leaves that chain's
// last-good Redis state in place until its TTL expires. Chains are published
// separately, so a mainnet subgraph outage cannot wipe the Sepolia view.
//
// Both sources for a chain are collected before anything is written, because
// the merge is what decides a venue's APY — publishing each source as it
// arrives would write the subgraph's stale rate and then overwrite it, and a
// reader between the two would see the wrong number.
func runCycle(ctx context.Context, st *store.Store, sources []source.Source, ttl time.Duration, tolerance float64, fetchTimeout time.Duration) {
	started := time.Now()
	var statuses []source.Status

	type batch struct {
		subgraph, live []venue.Venue
		served         bool
	}
	batches := map[string]*batch{}
	order := []string{}

	for _, src := range sources {
		chain := chainOf(src)
		b, seen := batches[chain]
		if !seen {
			b = &batch{}
			batches[chain], order = b, append(order, chain)
		}

		fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
		venues, err := src.Fetch(fetchCtx)
		cancel()
		if reporter, hasStatus := src.(interface{ Status() []source.Status }); hasStatus {
			statuses = append(statuses, reporter.Status()...)
		}
		if err != nil {
			// One source failing is survivable: the other still serves its own
			// protocols, and a stale rate for the rest is better than none.
			slog.Error("source fetch failed", "source", src.Name(), "chain", chain, "err", err)
			continue
		}
		b.served = true
		if _, isLive := src.(*source.RPCSource); isLive {
			b.live = append(b.live, venues...)
		} else {
			b.subgraph = append(b.subgraph, venues...)
		}
	}

	for _, chain := range order {
		b := batches[chain]
		if !b.served {
			slog.Error("every source failed, keeping last-good state", "chain", chain)
			continue
		}
		// Live rates win where both sources have the venue; the subgraph keeps
		// the reward and intrinsic legs it alone can measure.
		venues := source.Reconcile(b.subgraph, b.live, tolerance)

		// Same venue seen twice within one source: keep the higher APY.
		seen := map[string]venue.Venue{}
		for _, v := range venues {
			if prev, dup := seen[v.ID]; dup && prev.APY >= v.APY {
				continue
			}
			seen[v.ID] = v
		}
		merged := make([]venue.Venue, 0, len(seen))
		for _, v := range seen {
			merged = append(merged, v)
		}
		if err := st.Publish(ctx, chain, merged, ttl); err != nil {
			slog.Error("publish failed", "chain", chain, "err", err)
			continue
		}
		slog.Info("chain published", "chain", chain, "venues", len(merged),
			"subgraph", len(b.subgraph), "live", len(b.live))
	}

	// Adapter health is recorded even when no cycle produced anything usable:
	// an unconfigured chain has to be visible in /sources, not invisible.
	if err := st.PublishSources(ctx, statuses, ttl); err != nil {
		slog.Warn("publish source status failed", "err", err)
	}
	slog.Info("cycle complete", "sources", len(sources), "took", time.Since(started).String())
}

// chainOf reads the chain a source serves. Sources are per chain by
// construction; anything else would have no business writing venue keys.
func chainOf(src source.Source) string {
	switch s := src.(type) {
	case *source.GraphSource:
		return s.Chain.Label
	case *source.RPCSource:
		return s.Chain.Label
	}
	return ""
}
