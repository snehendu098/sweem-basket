package gas

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// ChainlinkETHUSDBase is the Chainlink ETH/USD aggregator proxy on Base
// MAINNET. The keeper no longer defaults to it — the address now comes from
// prices.FeedsFor(CHAIN_ID), so mainnet and Base Sepolia switch together. Kept
// as the documented mainnet value and used by the tests. Verified against the chain itself, not a doc: description() on this
// address returns "ETH / USD" and decimals() returns 8.
//
//	eth_call description()  0x7284e416 → "ETH / USD"
//	eth_call decimals()     0x313ce567 → 8
const ChainlinkETHUSDBase = "0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70"

// Selectors: first 4 bytes of keccak256 of the signature.
const (
	selLatestRoundData = "0xfeaf968c" // latestRoundData()
	selDecimals        = "0x313ce567" // decimals()
)

// EthCaller is the read-only chain access the feed needs.
type EthCaller interface {
	Call(ctx context.Context, to, data string) ([]byte, error)
}

// Chainlink reads ETH/USD from an onchain aggregator: no API key, no rate
// limit, and the staleness of the answer is visible in the answer itself.
type Chainlink struct {
	Caller  EthCaller
	Address string

	mu       sync.Mutex
	decimals int32
	haveDec  bool
}

// ETHUSD returns the latest round's price and the time the feed last updated.
func (c *Chainlink) ETHUSD(ctx context.Context) (float64, time.Time, error) {
	dec, err := c.Decimals(ctx)
	if err != nil {
		return 0, time.Time{}, err
	}
	raw, err := c.Caller.Call(ctx, c.Address, selLatestRoundData)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("chainlink: latestRoundData: %w", err)
	}
	// (roundId, answer, startedAt, updatedAt, answeredInRound), 32 bytes each.
	if len(raw) < 160 {
		return 0, time.Time{}, fmt.Errorf("chainlink: short latestRoundData response (%d bytes)", len(raw))
	}
	answer := new(big.Int).SetBytes(raw[32:64])
	// int256: the top bit set means a negative price, which is never valid here.
	if raw[32]&0x80 != 0 {
		return 0, time.Time{}, fmt.Errorf("chainlink: negative price")
	}
	updatedAt := new(big.Int).SetBytes(raw[96:128])

	price, _ := new(big.Float).Quo(
		new(big.Float).SetInt(answer),
		new(big.Float).SetFloat64(pow10(dec)),
	).Float64()
	return price, time.Unix(updatedAt.Int64(), 0).UTC(), nil
}

// Decimals reads and caches the feed's scale. Assuming 8 would be wrong the
// day someone points this at a different aggregator.
func (c *Chainlink) Decimals(ctx context.Context) (int32, error) {
	c.mu.Lock()
	if c.haveDec {
		d := c.decimals
		c.mu.Unlock()
		return d, nil
	}
	c.mu.Unlock()

	raw, err := c.Caller.Call(ctx, c.Address, selDecimals)
	if err != nil {
		return 0, fmt.Errorf("chainlink: decimals: %w", err)
	}
	if len(raw) < 32 {
		return 0, fmt.Errorf("chainlink: short decimals response: %s", hex.EncodeToString(raw))
	}
	d := new(big.Int).SetBytes(raw[:32]).Int64()
	if d < 0 || d > 36 {
		return 0, fmt.Errorf("chainlink: implausible decimals %d", d)
	}
	c.mu.Lock()
	c.decimals, c.haveDec = int32(d), true
	c.mu.Unlock()
	return int32(d), nil
}

func pow10(n int32) float64 {
	v := 1.0
	for range n {
		v *= 10
	}
	return v
}
