package gas

import (
	"context"
	"fmt"
)

type Set map[int]*Estimator

func (s Set) CostUSD(ctx context.Context, chainID int) (float64, error) {
	e, ok := s[chainID]
	if !ok || e == nil {
		return 0, fmt.Errorf("%w: no gas estimator configured for chain %d", ErrUnavailable, chainID)
	}
	return e.CostUSD(ctx)
}
