package gas

import (
	"context"
	"fmt"
)

// Set is one Estimator per chain id. Gas price and ETH price are both per
// chain, so pricing a Base Sepolia move off mainnet's node (or the reverse)
// produces a confident wrong number — the cheapest way to make the keeper churn
// or freeze. A chain with no estimator is an error, never another chain's.
type Set map[int]*Estimator

// CostUSD prices one rebalance on one chain.
func (s Set) CostUSD(ctx context.Context, chainID int) (float64, error) {
	e, ok := s[chainID]
	if !ok || e == nil {
		return 0, fmt.Errorf("%w: no gas estimator configured for chain %d", ErrUnavailable, chainID)
	}
	return e.CostUSD(ctx)
}
