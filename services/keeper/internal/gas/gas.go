// Package gas prices a rebalance in dollars from live chain data.
//
// Nothing here is guessed. Gas price comes from the Base node, ETH/USD comes
// from the Chainlink aggregator on Base, and the gas units are measured from
// real Base transactions (see Units below). If any input is missing or stale,
// this package returns an error — the keeper then skips the pass. A rebalance
// decided on fabricated cost data is worse than no rebalance.
package gas

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// ErrUnavailable means a cost input could not be obtained or trusted. Callers
// must skip, never substitute a default.
var ErrUnavailable = errors.New("gas: cost inputs unavailable")

// Measured gas units on Base mainnet, sampled from successful transactions
// around block 51,089,191 (2026-09-09). A rebalance is withdraw → approve →
// deposit, so the total is the sum of the three.
//
//	withdraw  Aave v3 Pool.withdraw()  170,504 – 348,276
//	          0x21d519409a22fc1084f0164a1f7dbc699b951276f91ef474548df85ebce497ef (170,504)
//	          0xc160895b962dc8f6689c41a4c6f2929f722f893ce533827c01fc94bf428b2568 (337,692)
//	          0x76da5f011b07dc4303e349573c028fa3cc0793190878d32a4695b5143804df05 (348,276)
//	approve   USDC.approve()            55,377 –  55,761
//	          0xd6f74b207344255558d44ee11d022052ba9adfc32b36f96103c866e96672064a (55,437)
//	          0x60417f8148fa526a430d65581548a455fc8b1c8700d0ad4b21a565f76896927e (55,761)
//	deposit   Aave v3 Pool.supply()    142,496 – 148,767
//	          0xd55b52076ec75e44e872cb0bc7717c097b0c6ba9c9186939f06404283c60764f (148,767)
//	          0x6724c52ba66441256bbf3de4d2a46be9807259207f4208aac2173d2dc46897cc (142,508)
//
// Withdraw spreads widely with accrued interest and reward claims, so the
// constant sits at the top of the observed range: over-pricing the move makes
// the keeper too cautious, under-pricing makes it churn.
const (
	UnitsWithdraw  uint64 = 350_000
	UnitsApprove   uint64 = 56_000
	UnitsDeposit   uint64 = 150_000
	UnitsRebalance        = UnitsWithdraw + UnitsApprove + UnitsDeposit // 556,000
)

// Sources, injected so the cost math can be tested without a node.
type (
	GasPricer interface {
		GasPriceWei(ctx context.Context) (*big.Int, error)
	}
	PriceFeed interface {
		// ETHUSD returns the price and the timestamp the feed last updated.
		ETHUSD(ctx context.Context) (price float64, updatedAt time.Time, err error)
	}
)

// Estimator turns live inputs into the USD cost of one rebalance leg.
type Estimator struct {
	Gas   GasPricer
	Feed  PriceFeed
	Units uint64
	// MaxAge rejects a price the feed has not refreshed recently.
	MaxAge time.Duration
	// CacheTTL keeps a pass over many subscriptions from hammering the node.
	CacheTTL time.Duration
	// Override forces a fixed USD cost. Tests and dry runs only — never a
	// fallback when the live fetch fails.
	Override float64
	Now      func() time.Time

	mu       sync.Mutex
	price    float64
	priceAt  time.Time // when we fetched it, for the cache
	priceSet bool
}

func (e *Estimator) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// CostUSD is the dollar cost of moving one position:
//
//	costUSD = gasUnits * gasPriceWei * ethPriceUSD / 1e18
//
// units are gas, gasPriceWei is wei per gas, ethPriceUSD is USD per ETH, and
// 1e18 converts wei to ETH. The result is USD.
func (e *Estimator) CostUSD(ctx context.Context) (float64, error) {
	if e.Override > 0 {
		return e.Override, nil
	}
	gasPrice, err := e.Gas.GasPriceWei(ctx)
	if err != nil {
		return 0, fmt.Errorf("%w: gas price: %v", ErrUnavailable, err)
	}
	if gasPrice.Sign() <= 0 {
		return 0, fmt.Errorf("%w: gas price is not positive", ErrUnavailable)
	}
	ethUSD, err := e.ethUSD(ctx)
	if err != nil {
		return 0, err
	}
	units := e.Units
	if units == 0 {
		units = UnitsRebalance
	}
	return CostUSD(units, gasPrice, ethUSD), nil
}

// CostUSD is the pure arithmetic, split out so it can be checked against a
// hand-computed example.
func CostUSD(units uint64, gasPriceWei *big.Int, ethPriceUSD float64) float64 {
	// big.Int for the wei product (it overflows float64 precision long before
	// it overflows range), then one conversion at the end.
	total := new(big.Int).Mul(new(big.Int).SetUint64(units), gasPriceWei)
	eth := new(big.Float).Quo(new(big.Float).SetInt(total), big.NewFloat(1e18))
	usd, _ := new(big.Float).Mul(eth, big.NewFloat(ethPriceUSD)).Float64()
	return usd
}

func (e *Estimator) ethUSD(ctx context.Context) (float64, error) {
	now := e.now()

	e.mu.Lock()
	if e.priceSet && e.CacheTTL > 0 && now.Sub(e.priceAt) < e.CacheTTL {
		p := e.price
		e.mu.Unlock()
		return p, nil
	}
	e.mu.Unlock()

	price, updatedAt, err := e.Feed.ETHUSD(ctx)
	if err != nil {
		return 0, fmt.Errorf("%w: eth price: %v", ErrUnavailable, err)
	}
	if price <= 0 {
		return 0, fmt.Errorf("%w: eth price is not positive", ErrUnavailable)
	}
	// A stale oracle answer is not permission to guess.
	if age := now.Sub(updatedAt); e.MaxAge > 0 && age > e.MaxAge {
		return 0, fmt.Errorf("%w: eth price is %s stale (max %s)", ErrUnavailable, age.Truncate(time.Second), e.MaxAge)
	}

	e.mu.Lock()
	e.price, e.priceAt, e.priceSet = price, now, true
	e.mu.Unlock()
	return price, nil
}
