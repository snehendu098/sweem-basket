package gas

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("gas: cost inputs unavailable")

const (
	// Top of the observed range, not the median: under-pricing makes the keeper
	// churn. Measured from real Base txs, withdraw spread 170k-348k.
	UnitsWithdraw  uint64 = 350_000
	UnitsApprove   uint64 = 56_000
	UnitsDeposit   uint64 = 150_000
	UnitsRebalance        = UnitsWithdraw + UnitsApprove + UnitsDeposit
)

type (
	GasPricer interface {
		GasPriceWei(ctx context.Context) (*big.Int, error)
	}
	PriceFeed interface {
		ETHUSD(ctx context.Context) (price float64, updatedAt time.Time, err error)
	}
)

type Estimator struct {
	Gas      GasPricer
	Feed     PriceFeed
	Units    uint64
	MaxAge   time.Duration
	CacheTTL time.Duration
	Override float64
	Now      func() time.Time

	mu       sync.Mutex
	price    float64
	priceAt  time.Time
	priceSet bool
}

func (e *Estimator) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

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

func CostUSD(units uint64, gasPriceWei *big.Int, ethPriceUSD float64) float64 {
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
	if age := now.Sub(updatedAt); e.MaxAge > 0 && age > e.MaxAge {
		return 0, fmt.Errorf("%w: eth price is %s stale (max %s)", ErrUnavailable, age.Truncate(time.Second), e.MaxAge)
	}

	e.mu.Lock()
	e.price, e.priceAt, e.priceSet = price, now, true
	e.mu.Unlock()
	return price, nil
}
