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

// Function selectors, all zero-argument or one-address, computed from the
// canonical signatures with keccak256 and confirmed against Base by calling
// them. Two callers share them: the venue generator, where every call is an
// identity check, and RPCSource, where they are the live rate read.
const (
	SelSymbol          = "0x95d89b41" // symbol()
	SelDecimals        = "0x313ce567" // decimals()
	SelTotalSupply     = "0x18160ddd" // totalSupply()
	SelAsset           = "0x38d52e0f" // asset()                ERC-4626
	SelBaseToken       = "0xc55dae63" // baseToken()            Comet
	SelUnderlying      = "0x6f307dc3" // underlying()           cToken
	SelIsMToken        = "0x699cd5e2" // isMToken()             Moonwell
	SelGetPool         = "0x026b1d5f" // getPool()              Aave addresses provider
	SelGetReservesList = "0xd1946dbc" // getReservesList()      Aave Pool
	SelGetReserveData  = "0x35ea6a75" // getReserveData(address) Aave Pool
	SelGetUtilization  = "0x7eb71131" // getUtilization()       Comet
	SelGetSupplyRate   = "0xd955759d" // getSupplyRate(uint256) Comet
	SelDescription     = "0x7284e416" // description()          Chainlink feed
	SelLatestRoundData = "0xfeaf968c" // latestRoundData()      Chainlink feed
	SelGetRoundData    = "0x9a6fc8f5" // getRoundData(uint80)   Chainlink feed
	SelGetSUSDSData    = "0x4a159379" // getSUSDSData()         Sky SSR oracle
)

// Caller is read-only access to one chain.
//
// Public Base endpoints rate-limit a burst of eth_calls, and a 429 is not
// evidence about an address — treating it as a failed read would silently
// shrink the venue set every time the node was busy. So transient failures are
// retried a bounded number of times with backoff, and every answer is
// memoised: the same getReservesList() serves every reserve of a pool.
//
// A Caller memoises forever, so the publisher builds a fresh one per cycle: a
// cached rate is a wrong rate five minutes later.
type Caller struct {
	rpc prices.RPC
	// Attempts bounds the retries. It is deliberately small for the publisher:
	// retrying into a rate limit earns a ban, and a missing venue for one cycle
	// is cheaper than a blocked node.
	Attempts int
	Pause    time.Duration
	// MinInterval paces calls that actually reach the node. Public Base
	// endpoints answer a burst of sixty eth_calls with 429s — measured, not
	// assumed — and a venue lost to a rate limit is a venue lost for the whole
	// cycle. Spreading the same calls over a few seconds costs nothing on a
	// five-minute poll and is the difference between reading every reserve and
	// reading one.
	// MinInterval is per CALL, not per request: mainnet.base.org counts the
	// calls inside a batch individually (measured: ~5 per second, batched or
	// not), so a ten-call batch has to wait ten intervals.
	MinInterval time.Duration
	// MaxBatch is the node's ceiling on one batch. mainnet.base.org answers an
	// eleven-call batch with "maximum 10 calls in 1 batch" and nothing else, so
	// an unchunked batch is a batch that returns nothing.
	MaxBatch int

	mu   sync.Mutex
	memo map[string]memoed

	// ponytail: one global gate, so calls are paced serially rather than by a
	// real token bucket. Per-host buckets only matter once a chain has more
	// than one node behind it.
	gate sync.Mutex
	next time.Time
}

func NewCaller(rpc prices.RPC) *Caller {
	return &Caller{rpc: rpc, memo: map[string]memoed{}, Attempts: 6, Pause: 250 * time.Millisecond, MaxBatch: 10}
}

// pace blocks until this Caller is allowed to spend n more calls on the node.
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

// memoed is one remembered answer. Permanent failures are remembered too: a
// reverting call reverts again, and asking twice only spends rate limit.
type memoed struct {
	raw string
	err error
}

func memoKey(to, data string) string { return strings.ToLower(to) + strings.ToLower(data) }

// Prefetch answers many calls in one round trip when the transport can batch,
// leaving the results in the memo for the ordinary Call path to pick up. It is
// a pure optimisation: skip it and every call still works, just one POST at a
// time — which is what earns a 429 on a public node.
//
// Transient per-call failures are deliberately NOT memoised, so the single-call
// path can still retry them.
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

// prefetchChunk runs one batch, retrying the whole thing while the node is
// merely busy.
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
			continue // leave it for the retrying single-call path
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

// IsTransient marks the node being busy, which says nothing about the address.
//
// "over rate limit" is in here because mainnet.base.org returns it as a
// per-call JSON-RPC error inside a 200 response rather than as a 429 — treating
// that as a permanent failure is exactly how a busy node silently deletes half
// the venue list.
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

// Uint reads a single uint256 return word.
func (c *Caller) Uint(ctx context.Context, to, sel string) (*big.Int, error) {
	ws, err := c.Words(ctx, to, sel, 1)
	if err != nil {
		return nil, err
	}
	return ws[0], nil
}

// Words splits a return into n 32-byte big-endian values. A struct of static
// fields (Aave's ReserveData) is encoded as exactly that flat sequence.
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

// Text reads a solidity string return, tolerating the bytes32 form some old
// tokens use.
func (c *Caller) Text(ctx context.Context, to, sel string) (string, error) {
	raw, err := c.Call(ctx, to, sel)
	if err != nil {
		return "", err
	}
	b, err := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
	if err != nil {
		return "", err
	}
	if len(b) < 64 { // bytes32-style
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

// AddressList reads an address[] return, used for Aave's getReservesList().
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

// AddressArg encodes one address argument.
func AddressArg(sel, addr string) string {
	return sel + strings.Repeat("0", 24) + strings.ToLower(strings.TrimPrefix(addr, "0x"))
}

// UintArg encodes one uint256 argument already held as a 32-byte word.
func UintArg(sel string, v *big.Int) string {
	h := v.Text(16)
	return sel + strings.Repeat("0", 64-len(h)) + h
}
