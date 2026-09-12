package prices

import (
	"context"
	"fmt"
)

// ErrNoChain means nothing is configured for the chain a caller asked about.
// It is deliberately not a fallback to another chain's client: a USDC price
// read off the wrong network is a wrong number, not a degraded one.
var ErrNoChain = fmt.Errorf("prices: no client configured for chain")

// Set holds one Client per chain id. Each Client is bound to its own verified
// feed table at construction, so a lookup cannot cross chains by accident.
type Set map[int]*Client

// For returns the client for a chain, or an error naming the chain.
func (s Set) For(chainID int) (*Client, error) {
	c, ok := s[chainID]
	if !ok || c == nil {
		return nil, fmt.Errorf("%w %d", ErrNoChain, chainID)
	}
	return c, nil
}

// USD prices one symbol on one chain.
func (s Set) USD(ctx context.Context, chainID int, symbol string) (Price, error) {
	c, err := s.For(chainID)
	if err != nil {
		return Price{}, err
	}
	return c.USD(ctx, symbol)
}
