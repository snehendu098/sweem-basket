package gas

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"
)

const ChainlinkETHUSDBase = "0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70"

const (
	selLatestRoundData = "0xfeaf968c"
	selDecimals        = "0x313ce567"
)

type EthCaller interface {
	Call(ctx context.Context, to, data string) ([]byte, error)
}

type Chainlink struct {
	Caller  EthCaller
	Address string

	mu       sync.Mutex
	decimals int32
	haveDec  bool
}

func (c *Chainlink) ETHUSD(ctx context.Context) (float64, time.Time, error) {
	dec, err := c.Decimals(ctx)
	if err != nil {
		return 0, time.Time{}, err
	}
	raw, err := c.Caller.Call(ctx, c.Address, selLatestRoundData)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("chainlink: latestRoundData: %w", err)
	}
	if len(raw) < 160 {
		return 0, time.Time{}, fmt.Errorf("chainlink: short latestRoundData response (%d bytes)", len(raw))
	}
	answer := new(big.Int).SetBytes(raw[32:64])
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
