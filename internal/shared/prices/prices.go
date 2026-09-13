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
	ErrNoFeed = errors.New("prices: no price feed for asset")
	ErrStale  = errors.New("prices: price feed is stale")
)

type Feed struct {
	Address   string
	Heartbeat time.Duration
}

const (
	ChainBaseMainnet = 8453
	ChainBaseSepolia = 84532
)

type Ratio struct {
	Feed  Feed
	Quote string
}

type table struct {
	Feeds  map[string]Feed
	Ratios map[string]Ratio
}

var chainTables = map[int]table{
	ChainBaseMainnet: {baseMainnetFeeds, baseMainnetRatios},
	ChainBaseSepolia: {baseSepoliaFeeds, nil},
}

func FeedsFor(chainID int) (map[string]Feed, map[string]Ratio) {
	t, ok := chainTables[chainID]
	if !ok {
		slog.Warn("prices: no verified feed table for chain; every asset will be unpriceable", "chain_id", chainID)
	}
	return t.Feeds, t.Ratios
}

// Heartbeat is per feed: stablecoin feeds publish every 24h, so a global 1h
// staleness bound marks a healthy USDC feed stale and deletes every USDC venue.
var baseMainnetFeeds = map[string]Feed{
	"USDC":   {"0x7e860098F58bBFC8648a4311b374B1D669a2bc6B", 24 * time.Hour},
	"USDBC":  {"0x7e860098F58bBFC8648a4311b374B1D669a2bc6B", 24 * time.Hour},
	"DAI":    {"0x591e79239a7d679378eC8c847e5038150364C78F", 24 * time.Hour},
	"ETH":    {"0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70", time.Hour},
	"WETH":   {"0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70", time.Hour},
	"CBETH":  {"0xd7818272B9e248357d13057AAb0B417aF31E817d", time.Hour},
	"CBBTC":  {"0x07DA0E54543a844a80ABE69c8A12F22B3aA59f9D", 2 * time.Hour},
	"WBTC":   {"0xCCADC697c55bbB68dc5bCdf8d3CBe83CdD4E071E", time.Hour},
	"USDT":   {"0xf19d560eB8d2ADf07BD6D13ed03e1D11215721F9", 24 * time.Hour},
	"USDS":   {"0x2330aaE3bca5F05169d5f4597964D44522F62930", 24 * time.Hour},
	"GHO":    {"0x42868EFcee13C0E71af89c04fF7d96f5bec479b0", 24 * time.Hour},
	"EURC":   {"0xDAe398520e2B67cd3f27aeF9Cf14D93D927f8250", 24 * time.Hour},
	"AERO":   {"0x4EC5970fC728C5f65ba413992CD5fF6FD70fcfF0", time.Hour},
	"TBTC":   {"0x6D75BFB5A5885f841b132198C9f0bE8c872057BF", 24 * time.Hour},
	"BTC":    {"0x64c911996D3c6aC71f9b455B1E8E7266BcbD848F", time.Hour},
	"AAVE":   {"0x65B5d02E1Fff839b8B67Fa26F8540e5f11454316", 24 * time.Hour},
	"MORPHO": {"0xe95e258bb6615d47515Fc849f8542dA651f12bF6", 24 * time.Hour},
	"VVV":    {"0xaABc55Ca55D70B034e4daA2551A224239890282F", 24 * time.Hour},
}

var baseSepoliaFeeds = map[string]Feed{
	"USDC": {"0xd30e2101a97dcbAeBCBC04F14C3f624E67A35165", 24 * time.Hour},
	"ETH":  {"0x4aDC67696bA383F43DD60A9e78F2C97Fbbfc7cb1", time.Hour},
	"WETH": {"0x4aDC67696bA383F43DD60A9e78F2C97Fbbfc7cb1", time.Hour},
}

var baseMainnetRatios = map[string]Ratio{
	"WSTETH": {Feed{"0x43a5C292A453A3bF3606fa856197f09D7B74251a", 24 * time.Hour}, "ETH"},
	"WEETH":  {Feed{"0xFC1415403EbB0c693f9a7844b92aD2Ff24775C65", 24 * time.Hour}, "ETH"},
	"RETH":   {Feed{"0xf397bF97280B488cA19ee3093E81C0a77F02e9a5", 24 * time.Hour}, "ETH"},
	"EZETH":  {Feed{"0x960BDD1dFD20d7c98fa482D793C3dedD73A113a3", 24 * time.Hour}, "ETH"},
}

const (
	selLatestRoundData = "0xfeaf968c"
	selDecimals        = "0x313ce567"
)

type RPC interface {
	Call(ctx context.Context, to, data string) (string, error)
}

type Price struct {
	USD       float64   `json:"usd"`
	UpdatedAt time.Time `json:"updated_at"`
}

type cached struct {
	p       Price
	fetched time.Time
}

type Client struct {
	rpc    RPC
	feeds  map[string]Feed
	ratios map[string]Ratio
	grace  time.Duration
	ttl    time.Duration

	mu       sync.Mutex
	cache    map[string]cached
	decimals map[string]int32
	now      func() time.Time
}

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
		at := quote.UpdatedAt
		if ratio.UpdatedAt.Before(at) {
			at = ratio.UpdatedAt
		}
		return Price{USD: ratio.USD * quote.USD, UpdatedAt: at}, nil
	}
	return Price{}, fmt.Errorf("%w: %s", ErrNoFeed, symbol)
}

func (c *Client) price(ctx context.Context, feed Feed) (Price, error) {
	addr := feed.Address

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

func decodeLatestRoundData(raw string) (answer *big.Int, updatedAt time.Time, err error) {
	ws, err := words(raw, 5)
	if err != nil {
		return nil, time.Time{}, err
	}
	return toSigned(ws[1]), time.Unix(ws[3].Int64(), 0).UTC(), nil
}

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

var twoPow256 = new(big.Int).Lsh(big.NewInt(1), 256)

func toSigned(v *big.Int) *big.Int {
	if v.BitLen() < 256 {
		return v
	}
	return new(big.Int).Sub(v, twoPow256)
}

func scale(answer *big.Int, decimals int32) float64 {
	f, _ := new(big.Float).SetInt(answer).Float64()
	return f / math.Pow10(int(decimals))
}

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
		return "", fmt.Errorf("eth_call: %s", out.Error.Message)
	}
	return out.Result, nil
}

type Call struct{ To, Data string }

type Result struct {
	Raw string
	Err error
}

type Batcher interface {
	CallBatch(ctx context.Context, calls []Call) ([]Result, error)
}

func (r *HTTPRPC) CallBatch(ctx context.Context, calls []Call) ([]Result, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	reqs := make([]map[string]any, len(calls))
	for i, c := range calls {
		reqs[i] = map[string]any{
			"jsonrpc": "2.0",
			"id":      i,
			"method":  "eth_call",
			"params":  []any{map[string]string{"to": c.To, "data": c.Data}, "latest"},
		}
	}
	body, err := json.Marshal(reqs)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("eth_call batch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("eth_call batch: status %d", resp.StatusCode)
	}

	var out []struct {
		ID     int    `json:"id"`
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("eth_call batch: decode: %w", err)
	}

	results := make([]Result, len(calls))
	for i := range results {
		results[i] = Result{Err: errors.New("eth_call batch: no answer for this call")}
	}
	for _, o := range out {
		if o.ID < 0 || o.ID >= len(results) {
			continue
		}
		if o.Error != nil {
			results[o.ID] = Result{Err: fmt.Errorf("eth_call: %s", o.Error.Message)}
			continue
		}
		results[o.ID] = Result{Raw: o.Result}
	}
	return results, nil
}
