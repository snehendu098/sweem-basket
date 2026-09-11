// Command keeper watches every subscriber's positions and triggers a rebalance
// when moving the money is worth more than it costs.
//
// It decides *when*. The wallet service still does *how*: the keeper never
// calls the executor and never signs anything, so it can be restarted,
// duplicated or crashed without putting funds at risk.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/config"
	"github.com/snehendu098/sweem-basket/internal/shared/httpx"
	"github.com/snehendu098/sweem-basket/internal/shared/prices"
	"github.com/snehendu098/sweem-basket/internal/shared/redisclient"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/client"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/engine"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/gas"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/policy"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/rpc"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/store"
)

// venuesUpdated is the channel market-data publishes to after each poll cycle.
const venuesUpdated = "venues:updated"

func main() {
	dryRunFlag := flag.Bool("dry-run", false, "evaluate and log everything, call nothing")
	flag.Parse()

	config.LoadDotEnv(config.GetEnv("ENV_FILE", ".env"))
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.Level(config.GetEnvInt("LOG_LEVEL", int(slog.LevelInfo))),
	}))
	slog.SetDefault(log)

	if err := run(log, *dryRunFlag); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, dryRunFlag bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	secret := config.GetEnv("KEEPER_SECRET", "")
	// Without a shared secret the keeper cannot authenticate to the wallet
	// service at all, so the only honest mode is dry run. Refuse to pretend.
	dryRun := dryRunFlag || config.GetEnv("DRY_RUN", "") == "true" || secret == ""
	if !dryRun && secret == "" {
		return errors.New("KEEPER_SECRET is required to run live; unset it to run in dry-run")
	}
	if secret == "" {
		log.Warn("KEEPER_SECRET unset: forcing dry run, no rebalance will be submitted")
	}

	db, err := store.New(ctx, config.GetEnv("DATABASE_URL", ""))
	if err != nil {
		return err
	}
	defer db.Close()

	// One RPC client serves both the gas pricing and the pending-execution
	// sweeper, so both read the node named by BASE_RPC_URL — the same variable
	// the executor uses.
	node := rpc.New(config.GetEnv("BASE_RPC_URL", "https://sepolia.base.org"))
	// The ETH/USD aggregator differs per chain, so it comes from the same
	// CHAIN_ID-selected table the price service uses instead of a second constant.
	ethUSD, _ := prices.FeedsFor(config.GetEnvInt("CHAIN_ID", prices.DefaultChainID))
	cost := &gas.Estimator{
		Gas: node,
		Feed: &gas.Chainlink{
			Caller:  node,
			Address: config.GetEnv("CHAINLINK_ETH_USD", ethUSD["ETH"].Address),
		},
		Units:    uint64(config.GetEnvInt("GAS_UNITS_REBALANCE", int(gas.UnitsRebalance))),
		MaxAge:   config.GetEnvDuration("PRICE_MAX_AGE", time.Hour),
		CacheTTL: config.GetEnvDuration("PRICE_CACHE_TTL", time.Minute),
		// Tests and dry runs only. Never a fallback for a failed live fetch.
		Override: config.GetEnvFloat("GAS_COST_USD", 0),
	}
	if cost.Override > 0 {
		log.Warn("GAS_COST_USD set: pricing rebalances off a fixed figure instead of live chain data",
			"gas_cost_usd", cost.Override)
	}

	eng := &engine.Engine{
		Store:    db,
		Market:   client.NewMarketData(config.GetEnv("MARKET_DATA_URL", "http://localhost:8081")),
		Wallet:   client.NewWallet(config.GetEnv("WALLET_URL", "http://localhost:8080"), secret),
		Cost:     cost,
		Receipts: node,
		Log:      log,
		Policy: policy.Params{
			MinDriftAPY: config.GetEnvFloat("MIN_DRIFT_APY", 0.5),
			// Testnet-scaled: a faucet-funded demo basket is a few dollars, and a
			// $50 floor would veto every rebalance as "too small to bother".
			MinPositionUSD: config.GetEnvFloat("MIN_POSITION_USD", 5),
			MinHold:        config.GetEnvDuration("MIN_HOLD_PERIOD", 6*time.Hour),
			RewardDiscount: config.GetEnvFloat("REWARD_DISCOUNT", 0.5),
			SafetyMargin:   config.GetEnvFloat("SAFETY_MARGIN", 1.5),
			// Counts legs, not passes: `executions` holds one row per leg, so a
			// 3-asset basket spends 3 of these on a single rebalance.
			MaxPerDay: config.GetEnvInt("MAX_REBALANCES_PER_DAY", 12),
			// Price a move over how long we expect to hold it, not over the
			// minimum we are allowed to. Defaulting to MinHold understates the
			// gain by orders of magnitude and only looked survivable because
			// Base gas is ~$0.01; the same math on an expensive chain never moves.
			Horizon: config.GetEnvDuration("BREAKEVEN_HORIZON", 30*24*time.Hour),
		},
		MinVenueTVL:   config.GetEnvFloat("MIN_VENUE_TVL_USD", 5_000), // testnet-scaled, see market-data MIN_TVL_USD
		DryRun:        dryRun,
		PendingMinAge: config.GetEnvDuration("PENDING_SWEEP_MIN_AGE", 2*time.Minute),
		PendingGiveUp: config.GetEnvDuration("PENDING_SWEEP_GIVE_UP", 24*time.Hour),
	}

	interval := config.GetEnvDuration("KEEPER_INTERVAL", 5*time.Minute)
	debounce := config.GetEnvDuration("KEEPER_DEBOUNCE", 30*time.Second)

	// Capacity 1: notifications arriving during a pass coalesce into exactly
	// one follow-up run instead of queueing up a stampede.
	trigger := make(chan string, 1)
	go watchVenues(ctx, log, trigger)

	go serveHealth(ctx, log, eng, db, dryRun)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Info("keeper started", "interval", interval, "debounce", debounce, "dry_run", dryRun)
	var last time.Time
	runPass := func(cause string) {
		if since := time.Since(last); since < debounce {
			log.Debug("debounced", "cause", cause, "since_last", since)
			return
		}
		last = time.Now()
		log.Info("pass start", "cause", cause)
		passCtx, cancel := context.WithTimeout(ctx, config.GetEnvDuration("KEEPER_PASS_TIMEOUT", 5*time.Minute))
		eng.Pass(passCtx)
		cancel()
	}

	runPass("startup")
	for {
		select {
		case <-ctx.Done():
			log.Info("keeper stopped")
			return nil
		case cause := <-trigger:
			runPass(cause)
		case <-ticker.C:
			runPass("tick")
		}
	}
}

// watchVenues turns market-data's pubsub into pass triggers. New rate data is
// the natural moment to re-evaluate; the periodic tick is only the safety net
// for when this subscription drops.
func watchVenues(ctx context.Context, log *slog.Logger, trigger chan<- string) {
	rdb, err := redisclient.New()
	if err != nil {
		log.Error("redis config; running on the timer alone", "err", err)
		return
	}
	defer rdb.Close()

	sub := rdb.Subscribe(ctx, venuesUpdated)
	defer sub.Close()
	log.Info("subscribed to venue updates", "channel", venuesUpdated)

	for msg := range sub.Channel() {
		var payload struct {
			Count int `json:"count"`
		}
		_ = json.Unmarshal([]byte(msg.Payload), &payload)
		select {
		case trigger <- fmt.Sprintf("venues:updated(count=%d)", payload.Count):
		default: // a pass is already pending; one is enough
		}
	}
}

func serveHealth(ctx context.Context, log *slog.Logger, eng *engine.Engine, db *store.Store, dryRun bool) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		st := eng.Stats()
		status, code, dbState := "ok", http.StatusOK, "up"
		if err := db.Ping(r.Context()); err != nil {
			status, dbState, code = "degraded", "down", http.StatusServiceUnavailable
		}
		httpx.JSON(w, code, map[string]any{
			"status":  status,
			"db":      dbState,
			"dry_run": dryRun,
			"stats":   st,
		})
	})

	srv := &http.Server{
		Addr:              config.GetEnv("KEEPER_ADDR", ":8083"),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("health listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("health server", "err", err)
	}
}
