package source

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/prices"
)

const (
	SelSymbol          = "0x95d89b41"
	SelDecimals        = "0x313ce567"
	SelTotalSupply     = "0x18160ddd"
	SelAsset           = "0x38d52e0f"
	SelBaseToken       = "0xc55dae63"
	SelUnderlying      = "0x6f307dc3"
	SelIsMToken        = "0x699cd5e2"
	SelGetPool         = "0x026b1d5f"
	SelGetReservesList = "0xd1946dbc"
	SelGetReserveData  = "0x35ea6a75"
	SelGetUtilization  = "0x7eb71131"
	SelGetSupplyRate   = "0xd955759d"
	SelDescription     = "0x7284e416"
	SelLatestRoundData = "0xfeaf968c"
	SelGetRoundData    = "0x9a6fc8f5"
	SelGetSUSDSData    = "0x4a159379"
)

type Caller struct {
	rpc         prices.RPC
	Attempts    int
	Pause       time.Duration
	MinInterval time.Duration
	MaxBatch    int

	mu   sync.Mutex
	memo map[string]memoed

	// ponytail: one global gate, not a token bucket. Per-host buckets only
	// matter once a chain has more than one node.
	gate sync.Mutex
	next time.Time
}

func NewCaller(rpc prices.RPC) *Caller {
	return &Caller{rpc: rpc, memo: map[string]memoed{}, Attempts: 6, Pause: 250 * time.Millisecond, MaxBatch: 10}
}

func (c *Caller) pace(ctx context.Context, n int) error {
	if c.MinInterval <= 0 {
		return nil
	}
	c.gate.Lock()
	defer c.gate.Unlock()
	if d := time.Until(c.next); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	c.next = time.Now().Add(time.Duration(n) * c.MinInterval)
	return nil
}

type memoed struct {
	raw string
	err error
}

func memoKey(to, data string) string { return strings.ToLower(to) + strings.ToLower(data) }

func (c *Caller) Prefetch(ctx context.Context, calls []prices.Call) {
	batcher, canBatch := c.rpc.(prices.Batcher)
	if !canBatch || len(calls) == 0 {
		return
	}
	pending := make([]prices.Call, 0, len(calls))
	c.mu.Lock()
	seen := make(map[string]bool, len(calls))
	for _, call := range calls {
		k := memoKey(call.To, call.Data)
		if _, have := c.memo[k]; have || seen[k] {
			continue
		}
		seen[k] = true
		pending = append(pending, call)
	}
	c.mu.Unlock()
	for len(pending) > 0 {
		chunk := pending
		if c.MaxBatch > 0 && len(chunk) > c.MaxBatch {
			chunk = chunk[:c.MaxBatch]
		}
		pending = pending[len(chunk):]
		c.prefetchChunk(ctx, batcher, chunk)
	}
}

func (c *Caller) prefetchChunk(ctx context.Context, batcher prices.Batcher, chunk []prices.Call) {
	var results []prices.Result
	for attempt := 0; attempt < c.Attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(c.Pause << (attempt - 1)):
			}
		}
		if err := c.pace(ctx, len(chunk)); err != nil {
			return
		}
		var err error
		if results, err = batcher.CallBatch(ctx, chunk); err == nil {
			break
		}
		if !IsTransient(err) {
			slog.Debug("eth_call batch failed, falling back to single calls", "err", err)
			return
		}
		results = nil
	}
	if results == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for i, r := range results {
		switch {
		case r.Err != nil && IsTransient(r.Err):
			continue
		case r.Err == nil && (r.Raw == "" || r.Raw == "0x"):
			r.Err = fmt.Errorf("empty return from %s", chunk[i].To)
		}
		c.memo[memoKey(chunk[i].To, chunk[i].Data)] = memoed{raw: r.Raw, err: r.Err}
	}
}

func (c *Caller) Call(ctx context.Context, to, data string) (string, error) {
	key := memoKey(to, data)
	c.mu.Lock()
	cached, ok := c.memo[key]
	c.mu.Unlock()
	if ok {
		return cached.raw, cached.err
	}

	var lastErr error
	for attempt := 0; attempt < c.Attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(c.Pause << (attempt - 1)):
			}
		}
		if err := c.pace(ctx, 1); err != nil {
			return "", err
		}
		out, err := c.rpc.Call(ctx, to, data)
		switch {
		case err != nil && IsTransient(err):
			lastErr = err
			continue
		case err != nil:
			return "", err
		case out == "" || out == "0x":
			return "", fmt.Errorf("empty return from %s", to)
		}
		c.mu.Lock()
		c.memo[key] = memoed{raw: out}
		c.mu.Unlock()
		return out, nil
	}
	return "", fmt.Errorf("%w (after %d attempts)", lastErr, c.Attempts)
}

func IsTransient(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "429") || strings.Contains(s, "status 5") ||
		strings.Contains(s, "rate limit") || strings.Contains(s, "too many requests") ||
		strings.Contains(s, "timeout") || strings.Contains(s, "eof")
}

func (c *Caller) Address(ctx context.Context, to, sel string) (string, error) {
	raw, err := c.Call(ctx, to, sel)
	if err != nil {
		return "", err
	}
	if len(raw) < 42 {
		return "", fmt.Errorf("short address return from %s", to)
	}
	return "0x" + strings.ToLower(raw[len(raw)-40:]), nil
}

func (c *Caller) Uint8(ctx context.Context, to, sel string) (int, error) {
	raw, err := c.Call(ctx, to, sel)
	if err != nil {
		return 0, err
	}
	n, ok := new(big.Int).SetString(strings.TrimPrefix(raw, "0x"), 16)
	if !ok || n.Sign() < 0 || n.Int64() > 36 {
		return 0, fmt.Errorf("implausible decimals from %s: %s", to, raw)
	}
	return int(n.Int64()), nil
}

func (c *Caller) Bool(ctx context.Context, to, sel string) (bool, error) {
	raw, err := c.Call(ctx, to, sel)
	if err != nil {
		return false, err
	}
	n, ok := new(big.Int).SetString(strings.TrimPrefix(raw, "0x"), 16)
	return ok && n.Sign() > 0, nil
}

func (c *Caller) Uint(ctx context.Context, to, sel string) (*big.Int, error) {
	ws, err := c.Words(ctx, to, sel, 1)
	if err != nil {
		return nil, err
	}
	return ws[0], nil
}

func (c *Caller) Words(ctx context.Context, to, data string, n int) ([]*big.Int, error) {
	raw, err := c.Call(ctx, to, data)
	if err != nil {
		return nil, err
	}
	h := strings.TrimPrefix(strings.TrimSpace(raw), "0x")
	if len(h) < n*64 {
		return nil, fmt.Errorf("short response from %s: %d hex chars, want %d", to, len(h), n*64)
	}
	out := make([]*big.Int, n)
	for i := range out {
		v, ok := new(big.Int).SetString(h[i*64:(i+1)*64], 16)
		if !ok {
			return nil, fmt.Errorf("undecodable word %d from %s", i, to)
		}
		out[i] = v
	}
	return out, nil
}

func (c *Caller) Text(ctx context.Context, to, sel string) (string, error) {
	raw, err := c.Call(ctx, to, sel)
	if err != nil {
		return "", err
	}
	b, err := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
	if err != nil {
		return "", err
	}
	if len(b) < 64 {
		return strings.TrimRight(string(b), "\x00"), nil
	}
	off := new(big.Int).SetBytes(b[:32]).Int64()
	if off+32 > int64(len(b)) {
		return "", fmt.Errorf("bad string offset from %s", to)
	}
	n := new(big.Int).SetBytes(b[off : off+32]).Int64()
	if off+32+n > int64(len(b)) {
		return "", fmt.Errorf("bad string length from %s", to)
	}
	return string(b[off+32 : off+32+n]), nil
}

func (c *Caller) AddressList(ctx context.Context, to, sel string) ([]string, error) {
	raw, err := c.Call(ctx, to, sel)
	if err != nil {
		return nil, err
	}
	b, err := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
	if err != nil || len(b) < 64 {
		return nil, fmt.Errorf("bad address[] return from %s", to)
	}
	n := int(new(big.Int).SetBytes(b[32:64]).Int64())
	if 64+n*32 > len(b) {
		return nil, fmt.Errorf("truncated address[] from %s", to)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		word := b[64+i*32 : 64+(i+1)*32]
		out = append(out, "0x"+hex.EncodeToString(word[12:]))
	}
	return out, nil
}

func AddressArg(sel, addr string) string {
	return sel + strings.Repeat("0", 24) + strings.ToLower(strings.TrimPrefix(addr, "0x"))
}

func UintArg(sel string, v *big.Int) string {
	h := v.Text(16)
	return sel + strings.Repeat("0", 64-len(h)) + h
}
