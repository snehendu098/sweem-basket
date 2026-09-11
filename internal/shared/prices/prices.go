// Package prices reads USD prices from Chainlink aggregators on Base.
//
// Onchain, no API key, no rate limit, and the same source the lending venues
// price against. Nothing here ever invents a number: an asset with no feed, a
// stale round, or a non-positive answer is reported as unavailable, never
// defaulted to a dollar.
package prices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNoFeed means we have no verified aggregator for this symbol. The
	// caller must report the value as unknown, not guess it.
	ErrNoFeed = errors.New("prices: no price feed for asset")
	// ErrStale means the feed answered but the round is older than we accept.
	ErrStale = errors.New("prices: price feed is stale")
)

// Feed is one Chainlink aggregator proxy and how often it actually publishes.
type Feed struct {
	Address string
	// Heartbeat is the feed's own publication cadence, measured onchain by
	// walking getRoundData over recent rounds — not copied from a doc page.
	// A round older than Heartbeat plus the configured grace is stale.
	Heartbeat time.Duration
}

// Chain ids we have verified feed tables for. One env var (CHAIN_ID) selects
// the table for the whole process; nothing else in the codebase branches on it.
const (
	ChainBaseMainnet = 8453
	ChainBaseSepolia = 84532
	// DefaultChainID is Base Sepolia: the deployment target today. Flip this (or
	// set CHAIN_ID=8453) to go back to mainnet — the mainnet table below is kept
	// intact and verified for exactly that.
	DefaultChainID = ChainBaseSepolia
)

// Ratio is an asset priced as ratio x quote, for assets with no direct USD feed.
type Ratio struct {
	Feed  Feed
	Quote string // symbol the ratio is denominated in; must exist in the same chain's feeds
}

// table is one chain's complete price universe. An asset absent from it is
// unpriceable on that chain, which callers must surface as unavailable.
type table struct {
	Feeds  map[string]Feed
	Ratios map[string]Ratio
}

var chainTables = map[int]table{
	ChainBaseMainnet: {baseMainnetFeeds, baseMainnetRatios},
	// Base Sepolia has exactly two Chainlink feeds relevant to us. Everything
	// else is deliberately unpriceable there — no proxying a testnet asset onto
	// a mainnet feed, no defaulting a stablecoin to $1.
	ChainBaseSepolia: {baseSepoliaFeeds, nil},
}

// FeedsFor returns the verified tables for a chain. An unknown chain yields
// empty tables: every asset then reports ErrNoFeed, which is loud and correct.
func FeedsFor(chainID int) (map[string]Feed, map[string]Ratio) {
	t, ok := chainTables[chainID]
	if !ok {
		slog.Warn("prices: no verified feed table for chain; every asset will be unpriceable", "chain_id", chainID)
	}
	return t.Feeds, t.Ratios
}

// baseMainnetFeeds maps an asset symbol to its aggregator on Base mainnet.
//
// Every address was verified by calling description() on Base mainnet; the
// comment is the exact string the contract returned, followed by the observed
// gaps between the last few rounds. Add a symbol only after doing the same —
// an unverified address is a fabricated price.
//
// Note the two very different cadences. Stablecoin feeds publish on a 24h
// heartbeat (deviation-triggered in between), so a single global max-age of an
// hour would mark a perfectly healthy USDC feed stale and make every USDC
// basket unroutable. That is why the bound is per feed.
var baseMainnetFeeds = map[string]Feed{
	// "USDC / USD" — measured gaps: 24.00, 24.00, 24.00, 24.01 h
	"USDC": {"0x7e860098F58bBFC8648a4311b374B1D669a2bc6B", 24 * time.Hour},
	// bridged USDC, same aggregator
	"USDBC": {"0x7e860098F58bBFC8648a4311b374B1D669a2bc6B", 24 * time.Hour},
	// "DAI / USD" — measured gaps: 24.01, 24.00, 24.00, 24.00 h
	"DAI": {"0x591e79239a7d679378eC8c847e5038150364C78F", 24 * time.Hour},
	// "ETH / USD" — measured gaps: 0.07, 0.02, 0.03, 0.04 h
	"ETH": {"0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70", time.Hour},
	// WETH redeems 1:1 for ETH by contract, so the ETH feed is exact, not an
	// approximation.
	"WETH": {"0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70", time.Hour},
	// "CBETH / USD" — measured gaps: 0.08, 0.01, 0.13 h
	"CBETH": {"0xd7818272B9e248357d13057AAb0B417aF31E817d", time.Hour},
	// "cbBTC / USD" — measured gaps: 0.63, 0.27 h
	"CBBTC": {"0x07DA0E54543a844a80ABE69c8A12F22B3aA59f9D", 2 * time.Hour},
	// "WBTC / USD" — measured gaps: 0.03, 0.07, 0.02, 0.01 h
	"WBTC": {"0xCCADC697c55bbB68dc5bCdf8d3CBe83CdD4E071E", time.Hour},
	// "USDT / USD" — measured gaps: 24.00, 24.00, 24.01, 24.01 h
	"USDT": {"0xf19d560eB8d2ADf07BD6D13ed03e1D11215721F9", 24 * time.Hour},
	// "USDS / USD" — measured gaps: 24.01, 24.00, 24.00, 24.01 h
	"USDS": {"0x2330aaE3bca5F05169d5f4597964D44522F62930", 24 * time.Hour},
	// "GHO / USD" — measured gaps: 24.00, 24.00, 24.00, 24.00 h
	"GHO": {"0x42868EFcee13C0E71af89c04fF7d96f5bec479b0", 24 * time.Hour},
	// "EURC / USD" — measured gaps: 24.00, 24.00, 24.00, 24.00 h.
	// Note this is EURC itself, not EUR/USD: a EURC depeg is visible here.
	"EURC": {"0xDAe398520e2B67cd3f27aeF9Cf14D93D927f8250", 24 * time.Hour},
	// "AERO / USD" — measured gaps: 0.11, 0.04, 0.26, 0.23, 0.47 h
	"AERO": {"0x4EC5970fC728C5f65ba413992CD5fF6FD70fcfF0", time.Hour},
	// "TBTC / USD" — deviation-driven and irregular; measured gaps ranged
	// 0.21 .. 13.47 h over the last 12 rounds, so the bound is the 24h heartbeat.
	"TBTC": {"0x6D75BFB5A5885f841b132198C9f0bE8c872057BF", 24 * time.Hour},
	// "BTC / USD" — measured gaps: 0.07, 0.04, 0.03, 0.07, 0.01 h
	"BTC": {"0x64c911996D3c6aC71f9b455B1E8E7266BcbD848F", time.Hour},
}

// baseSepoliaFeeds is the whole of Base Sepolia's usable Chainlink coverage.
// Both addresses verified by calling description()/decimals()/latestRoundData()
// on chain 84532; the comment is the exact description() string.
var baseSepoliaFeeds = map[string]Feed{
	// "USDC / USD" — 24h heartbeat, same cadence as mainnet stablecoin feeds.
	"USDC": {"0xd30e2101a97dcbAeBCBC04F14C3f624E67A35165", 24 * time.Hour},
	// "ETH / USD" — 1h heartbeat.
	"ETH": {"0x4aDC67696bA383F43DD60A9e78F2C97Fbbfc7cb1", time.Hour},
	// WETH redeems 1:1 for ETH by contract, so the ETH feed is exact.
	"WETH": {"0x4aDC67696bA383F43DD60A9e78F2C97Fbbfc7cb1", time.Hour},
}

// baseMainnetRatios covers assets with no direct USD aggregator on Base, only an
// onchain ratio against ETH. The USD price is ratio x quote, and BOTH legs are
// staleness-checked against their own heartbeat: a fresh ETH price does not
// excuse a stale wstETH ratio. Using the plain ETH price for a wrapped staking
// token would silently overstate nothing today and understate it by the whole
// accrued yield tomorrow, so it is not done.
//
// Same verification rule as Feeds: description() called on Base, string quoted.
var baseMainnetRatios = map[string]Ratio{
	// "WSTETH / ETH" — measured gaps: 24.00 x4
	"WSTETH": {Feed{"0x43a5C292A453A3bF3606fa856197f09D7B74251a", 24 * time.Hour}, "ETH"},
	// "weETH / ETH" — measured gaps: 24.01, 24.01, 24.00, 24.00 h
	"WEETH": {Feed{"0xFC1415403EbB0c693f9a7844b92aD2Ff24775C65", 24 * time.Hour}, "ETH"},
	// "RETH / ETH" — measured gaps: 24.00, 24.01, 24.00, 24.01 h
	"RETH": {Feed{"0xf397bF97280B488cA19ee3093E81C0a77F02e9a5", 24 * time.Hour}, "ETH"},
	// "ezETH / ETH" — measured gaps: 24.01, 24.00, 24.00, 24.00 h
	"EZETH": {Feed{"0x960BDD1dFD20d7c98fa482D793C3dedD73A113a3", 24 * time.Hour}, "ETH"},
}

// Function selectors. Both take no arguments, so there is nothing to encode.
const (
	selLatestRoundData = "0xfeaf968c"
	selDecimals        = "0x313ce567"
)

// RPC is the eth_call transport, injected so tests never touch the network.
type RPC interface {
	Call(ctx context.Context, to, data string) (string, error)
}

// Price is a value we actually read, with the round time that produced it.
type Price struct {
	USD       float64   `json:"usd"`
	UpdatedAt time.Time `json:"updated_at"`
}

type cached struct {
	p       Price
	fetched time.Time
}

type Client struct {
	rpc RPC
	// feeds/ratios are the chain's table, resolved once at construction so a
	// price lookup can never read another chain's addresses.
	feeds  map[string]Feed
	ratios map[string]Ratio
	grace  time.Duration // slack allowed on top of a feed's own heartbeat
	ttl    time.Duration // how long we reuse a fetched price

	mu       sync.Mutex
	cache    map[string]cached
	decimals map[string]int32 // per aggregator; immutable in practice
	// now is overridable so staleness is testable without sleeping.
	now func() time.Time
}

// New builds a client for one chain. grace is the slack allowed on top of each
// feed's measured heartbeat before its round counts as stale.
func New(rpc RPC, chainID int, grace, ttl time.Duration) *Client {
	feeds, ratios := FeedsFor(chainID)
	return &Client{
		rpc:      rpc,
		feeds:    feeds,
		ratios:   ratios,
		grace:    grace,
		ttl:      ttl,
		cache:    map[string]cached{},
		decimals: map[string]int32{},
		now:      time.Now,
	}
}

// USD returns the price of one unit of symbol. Unknown symbols, stale rounds
// and RPC failures are all errors — there is no fallback value.
func (c *Client) USD(ctx context.Context, symbol string) (Price, error) {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	if feed, ok := c.feeds[sym]; ok {
		return c.price(ctx, feed)
	}
	if r, ok := c.ratios[sym]; ok {
		ratio, err := c.price(ctx, r.Feed)
		if err != nil {
			return Price{}, fmt.Errorf("prices: %s ratio: %w", sym, err)
		}
		quote, err := c.USD(ctx, r.Quote)
		if err != nil {
			return Price{}, fmt.Errorf("prices: %s quote %s: %w", sym, r.Quote, err)
		}
		// The composite is only as fresh as its stalest leg.
		at := quote.UpdatedAt
		if ratio.UpdatedAt.Before(at) {
			at = ratio.UpdatedAt
		}
		return Price{USD: ratio.USD * quote.USD, UpdatedAt: at}, nil
	}
	return Price{}, fmt.Errorf("%w: %s", ErrNoFeed, symbol)
}

// price reads one aggregator, through the TTL cache.
func (c *Client) price(ctx context.Context, feed Feed) (Price, error) {
	addr := feed.Address

	// One portfolio request touches the same feed repeatedly; the TTL keeps
	// that to a single eth_call.
	c.mu.Lock()
	if hit, ok := c.cache[addr]; ok && c.now().Sub(hit.fetched) < c.ttl {
		c.mu.Unlock()
		return hit.p, nil
	}
	c.mu.Unlock()

	p, err := c.fetch(ctx, feed)
	if err != nil {
		return Price{}, err
	}
	c.mu.Lock()
	c.cache[addr] = cached{p: p, fetched: c.now()}
	c.mu.Unlock()
	return p, nil
}

func (c *Client) fetch(ctx context.Context, feed Feed) (Price, error) {
	dec, err := c.feedDecimals(ctx, feed.Address)
	if err != nil {
		return Price{}, err
	}
	raw, err := c.rpc.Call(ctx, feed.Address, selLatestRoundData)
	if err != nil {
		return Price{}, fmt.Errorf("prices: latestRoundData %s: %w", feed.Address, err)
	}
	answer, updatedAt, err := decodeLatestRoundData(raw)
	if err != nil {
		return Price{}, err
	}
	if answer.Sign() <= 0 {
		return Price{}, fmt.Errorf("prices: %s: non-positive answer", feed.Address)
	}
	if age := c.now().Sub(updatedAt); age > feed.Heartbeat+c.grace {
		return Price{}, fmt.Errorf("%w: %s last updated %s ago", ErrStale, feed.Address, age.Truncate(time.Second))
	}
	return Price{USD: scale(answer, dec), UpdatedAt: updatedAt}, nil
}

// feedDecimals reads decimals() from the aggregator rather than assuming 8.
func (c *Client) feedDecimals(ctx context.Context, feed string) (int32, error) {
	c.mu.Lock()
	if d, ok := c.decimals[feed]; ok {
		c.mu.Unlock()
		return d, nil
	}
	c.mu.Unlock()

	raw, err := c.rpc.Call(ctx, feed, selDecimals)
	if err != nil {
		return 0, fmt.Errorf("prices: decimals %s: %w", feed, err)
	}
	words, err := words(raw, 1)
	if err != nil {
		return 0, err
	}
	d := words[0].Int64()
	if d < 0 || d > 36 {
		return 0, fmt.Errorf("prices: %s: implausible decimals %d", feed, d)
	}
	c.mu.Lock()
	c.decimals[feed] = int32(d)
	c.mu.Unlock()
	return int32(d), nil
}

// decodeLatestRoundData pulls answer (word 1) and updatedAt (word 3) out of the
// five returned values: roundId, answer, startedAt, updatedAt, answeredInRound.
func decodeLatestRoundData(raw string) (answer *big.Int, updatedAt time.Time, err error) {
	ws, err := words(raw, 5)
	if err != nil {
		return nil, time.Time{}, err
	}
	return toSigned(ws[1]), time.Unix(ws[3].Int64(), 0).UTC(), nil
}

// words splits a 0x-prefixed eth_call result into n 32-byte big-endian values.
func words(raw string, n int) ([]*big.Int, error) {
	h := strings.TrimPrefix(strings.TrimSpace(raw), "0x")
	if len(h) < n*64 {
		return nil, fmt.Errorf("prices: short response: %d hex chars, want %d", len(h), n*64)
	}
	out := make([]*big.Int, n)
	for i := range out {
		v, ok := new(big.Int).SetString(h[i*64:(i+1)*64], 16)
		if !ok {
			return nil, fmt.Errorf("prices: undecodable word %d", i)
		}
		out[i] = v
	}
	return out, nil
}

// twoPow256 is the modulus for int256 two's-complement.
var twoPow256 = new(big.Int).Lsh(big.NewInt(1), 256)

// toSigned reinterprets a 256-bit word as int256. answer is signed, and a
// negative answer must surface as negative so it can be rejected, not wrap to
// an astronomical price.
func toSigned(v *big.Int) *big.Int {
	if v.BitLen() < 256 {
		return v
	}
	return new(big.Int).Sub(v, twoPow256)
}

// scale converts a fixed-point feed answer to a float using the feed's own
// decimals.
func scale(answer *big.Int, decimals int32) float64 {
	f, _ := new(big.Float).SetInt(answer).Float64()
	return f / math.Pow10(int(decimals))
}

// --- transport ---

// HTTPRPC is a minimal eth_call client. Hand-rolled: a JSON-RPC POST is not
// worth a dependency.
type HTTPRPC struct {
	URL  string
	HTTP *http.Client
}

func NewHTTPRPC(url string) *HTTPRPC {
	return &HTTPRPC{URL: url, HTTP: &http.Client{Timeout: 8 * time.Second}}
}

func (r *HTTPRPC) Call(ctx context.Context, to, data string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "eth_call",
		"params":  []any{map[string]string{"to": to, "data": data}, "latest"},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("eth_call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("eth_call: status %d", resp.StatusCode)
	}

	var out struct {
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("eth_call: decode: %w", err)
	}
	if out.Error != nil {
		// Public RPCs rate-limit; that is an unavailable price, not a zero one.
		return "", fmt.Errorf("eth_call: %s", out.Error.Message)
	}
	return out.Result, nil
}
