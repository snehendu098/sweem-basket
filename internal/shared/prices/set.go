package prices

import (
	"context"
	"fmt"
)

var ErrNoChain = fmt.Errorf("prices: no client configured for chain")

type Set map[int]*Client

func (s Set) USD(ctx context.Context, chainID int, symbol string) (Price, error) {
	c, ok := s[chainID]
	if !ok || c == nil {
		return Price{}, fmt.Errorf("%w %d", ErrNoChain, chainID)
	}
	return c.USD(ctx, symbol)
}
