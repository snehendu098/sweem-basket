package rpc

import (
	"context"
	"fmt"
)

// Set is one node client per chain id. A receipt looked up on the wrong chain
// is never found, which reads as "still pending" forever rather than as the
// misconfiguration it is — so an unknown chain is an explicit error.
type Set map[int]*Client

// TransactionReceipt reads a receipt from the chain the transaction was sent on.
func (s Set) TransactionReceipt(ctx context.Context, chainID int, txHash string) (*Receipt, error) {
	c, ok := s[chainID]
	if !ok || c == nil {
		return nil, fmt.Errorf("no rpc node configured for chain %d", chainID)
	}
	return c.TransactionReceipt(ctx, txHash)
}
