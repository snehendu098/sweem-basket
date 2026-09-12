package prices

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// deadRPC answers nothing: these tests are about which table is consulted,
// which is decided before any call is made.
type deadRPC struct{}

func (deadRPC) Call(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("no node")
}

// A price must come from the asked-for chain's table or not at all. Base
// Sepolia has no cbBTC feed; mainnet does. Asking Sepolia must fail rather
// than quietly answer from the mainnet table.
func TestSetSelectsPerChainFeedTable(t *testing.T) {
	set := Set{
		ChainBaseMainnet: New(deadRPC{}, ChainBaseMainnet, time.Hour, time.Minute),
		ChainBaseSepolia: New(deadRPC{}, ChainBaseSepolia, time.Hour, time.Minute),
	}
	if _, err := set.USD(context.Background(), ChainBaseSepolia, "CBBTC"); !errors.Is(err, ErrNoFeed) {
		t.Fatalf("sepolia cbBTC: got %v, want ErrNoFeed", err)
	}
	if _, err := set.USD(context.Background(), 1, "USDC"); !errors.Is(err, ErrNoChain) {
		t.Fatalf("unconfigured chain: got %v, want ErrNoChain", err)
	}
	// Mainnet does know cbBTC, so it gets as far as the (nil) RPC instead of
	// reporting the asset unknown.
	if _, err := set.USD(context.Background(), ChainBaseMainnet, "CBBTC"); errors.Is(err, ErrNoFeed) {
		t.Fatal("mainnet cbBTC reported as having no feed")
	}
}
