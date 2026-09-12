package rpc

import (
	"context"
	"fmt"
)

type Set map[int]*Client

func (s Set) TransactionReceipt(ctx context.Context, chainID int, txHash string) (*Receipt, error) {
	c, ok := s[chainID]
	if !ok || c == nil {
		return nil, fmt.Errorf("no rpc node configured for chain %d", chainID)
	}
	return c.TransactionReceipt(ctx, txHash)
}
