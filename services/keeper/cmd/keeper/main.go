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

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
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

func rpcURL(chainID int) string {
	def := "https://mainnet.base.org"
	if chainID == chains.BaseSepolia {
		def = "https://sepolia.base.org"
	}
	return config.GetEnv(fmt.Sprintf("BASE_RPC_URL_%d", chainID), def)
}

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

	nodes := rpc.Set{}
	costs := gas.Set{}
	override := config.GetEnvFloat("GAS_COST_USD", 0)
	for _, chainID := range chains.Supported() {
		node := rpc.New(rpcURL(chainID))
		nodes[chainID] = node
		ethUSD, _ := prices.FeedsFor(chainID)
		feed := ethUSD["ETH"].Address
		if feed == "" {
			return fmt.Errorf("no verified ETH/USD feed for chain %d", chainID)
		}
		costs[chainID] = &gas.Estimator{
			Gas:      node,
			Feed:     &gas.Chainlink{Caller: node, Address: feed},
			Units:    uint64(config.GetEnvInt("GAS_UNITS_REBALANCE", int(gas.UnitsRebalance))),
			MaxAge:   config.GetEnvDuration("PRICE_MAX_AGE", time.Hour),
			CacheTTL: config.GetEnvDuration("PRICE_CACHE_TTL", time.Minute),
			Override: override,
		}
	}
	if override > 0 {
		log.Warn("GAS_COST_USD set: pricing rebalances off a fixed figure instead of live chain data",
			"gas_cost_usd", override)
	}
	log.Info("chains configured", "chains", chains.Supported())

	eng := &engine.Engine{
		Store:    db,
		Market:   client.NewMarketData(config.GetEnv("MARKET_DATA_URL", "http://localhost:8081")),
		Wallet:   client.NewWallet(config.GetEnv("WALLET_URL", "http://localhost:8080"), secret),
		Cost:     costs,
		Receipts: nodes,
		Log:      log,
		Policy: policy.Params{
			MinDriftAPY:    config.GetEnvFloat("MIN_DRIFT_APY", 0.5),
			MinPositionUSD: config.GetEnvFloat("MIN_POSITION_USD", 5),
			MinHold:        config.GetEnvDuration("MIN_HOLD_PERIOD", 6*time.Hour),
			RewardDiscount: config.GetEnvFloat("REWARD_DISCOUNT", 0.5),
			SafetyMargin:   config.GetEnvFloat("SAFETY_MARGIN", 1.5),
			MaxPerDay:      config.GetEnvInt("MAX_REBALANCES_PER_DAY", 12),
			Horizon:        config.GetEnvDuration("BREAKEVEN_HORIZON", 30*24*time.Hour),
		},
		MinVenueTVL:   config.GetEnvFloat("MIN_VENUE_TVL_USD", 5_000),
		DryRun:        dryRun,
		PendingMinAge: config.GetEnvDuration("PENDING_SWEEP_MIN_AGE", 2*time.Minute),
		PendingGiveUp: config.GetEnvDuration("PENDING_SWEEP_GIVE_UP", 24*time.Hour),
	}

	interval := config.GetEnvDuration("KEEPER_INTERVAL", 5*time.Minute)
	debounce := config.GetEnvDuration("KEEPER_DEBOUNCE", 30*time.Second)

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
		default:
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
