// Command api runs the wallet service: auth, baskets, portfolio, and the only
// client of the executor.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/config"
	"github.com/snehendu098/sweem-basket/internal/shared/prices"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/api"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/auth"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/executor"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/marketdata"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/store"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/tokenapi"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	config.LoadDotEnv(".env")

	var err error
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	appID := config.GetEnv("PRIVY_APP_ID", "")

	// The verification key normally comes from Privy's API at boot, so there is
	// one less thing to configure and it cannot go stale. PRIVY_VERIFICATION_KEY
	// overrides the fetch for offline work and tests.
	key := config.GetEnv("PRIVY_VERIFICATION_KEY", "")
	if key == "" {
		fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		key, err = auth.FetchVerificationKey(fetchCtx, &http.Client{Timeout: 10 * time.Second},
			appID, config.GetEnv("PRIVY_APP_SECRET", ""))
		cancel()
		if err != nil {
			// Fail closed. A service that cannot verify tokens must not start.
			return err
		}
		log.Info("fetched privy verification key")
	}

	// Fail loudly on missing auth config rather than starting an open service.
	verifier, err := auth.NewVerifier(key, appID)
	if err != nil {
		return err
	}

	db, err := store.New(ctx, config.GetEnv("DATABASE_URL", ""))
	if err != nil {
		return err
	}
	defer db.Close()

	srv := &api.Server{
		Store:    db,
		Market:   marketdata.New(config.GetEnv("MARKET_DATA_URL", "http://localhost:8081")),
		Executor: executor.New(config.GetEnv("EXECUTOR_URL", "http://localhost:8082")),
		Auth:     verifier,
		TokenAPI: tokenapi.New(
			config.GetEnv("TOKEN_API_URL", tokenapi.DefaultBaseURL),
			config.GetEnv("TOKEN_API_JWT", ""),
		),
		Prices: prices.New(
			prices.NewHTTPRPC(config.GetEnv("BASE_RPC_URL", "https://sepolia.base.org")),
			config.GetEnvInt("CHAIN_ID", prices.DefaultChainID), // selects the whole feed table
			config.GetEnvDuration("PRICE_MAX_AGE", time.Hour),   // grace atop each feed's heartbeat
			config.GetEnvDuration("PRICE_CACHE_TTL", time.Minute),
		),
		Log: log,
		// Testnet-scaled, same reason as market-data's MIN_TVL_USD: Base Sepolia
		// venues hold six figures at most.
		MinVenueTVL:           config.GetEnvFloat("MIN_VENUE_TVL_USD", 50_000),
		DefaultChain:          config.GetEnv("DEFAULT_CHAIN", "Base"),
		RebalanceThresholdAPY: config.GetEnvFloat("REBALANCE_THRESHOLD_APY", 0.5),
		MaxSlippageBps:        config.GetEnvInt("MAX_SLIPPAGE_BPS", 50),
	}

	httpSrv := &http.Server{
		Addr:              config.GetEnv("WALLET_ADDR", ":8080"),
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      90 * time.Second, // executor calls can be slow
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("wallet service listening", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}
