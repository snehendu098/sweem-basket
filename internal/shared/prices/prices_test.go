package prices

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"
)

// fakeRPC answers by selector. No network, ever.
type fakeRPC struct {
	byData map[string]string
	err    error
	calls  int
}

func (f *fakeRPC) Call(_ context.Context, _, data string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.byData[data]
	if !ok {
		return "", fmt.Errorf("unexpected selector %s", data)
	}
	return v, nil
}

func word(v *big.Int) string {
	h := new(big.Int).Mod(v, twoPow256).Text(16)
	return strings.Repeat("0", 64-len(h)) + h
}

// roundData builds a latestRoundData return: roundId, answer, startedAt,
// updatedAt, answeredInRound.
func roundData(answer *big.Int, updatedAt time.Time) string {
	return "0x" + word(big.NewInt(42)) + word(answer) +
		word(big.NewInt(updatedAt.Unix())) + word(big.NewInt(updatedAt.Unix())) + word(big.NewInt(42))
}

func decimalsWord(d int64) string { return "0x" + word(big.NewInt(d)) }

// Tests exercise the mainnet table: it is the larger one and covers ratio feeds.
func newTestClient(rpc RPC, now time.Time) *Client {
	c := New(rpc, ChainBaseMainnet, time.Hour, time.Minute)
	c.now = func() time.Time { return now }
	return c
}

func TestUSD(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-5 * time.Minute)
	// USDC has a 24h heartbeat; ETH has 1h. Staleness is judged per feed.
	staleForUSDC := now.Add(-30 * time.Hour)
	staleForETH := now.Add(-3 * time.Hour)

	tests := []struct {
		name      string
		symbol    string
		decimals  int64
		answer    *big.Int
		updatedAt time.Time
		rpcErr    error
		want      float64
		wantErr   error
	}{
		{
			name: "8-decimal feed", symbol: "USDC", decimals: 8,
			answer: big.NewInt(99990801), updatedAt: fresh, want: 0.99990801,
		},
		{
			name: "8-decimal feed, four figures", symbol: "ETH", decimals: 8,
			answer: big.NewInt(249057000000), updatedAt: fresh, want: 2490.57,
		},
		{
			// decimals() is read, not assumed: the same integer must scale
			// differently for an 18-decimal feed.
			name: "18-decimal feed scales by its own decimals", symbol: "ETH", decimals: 18,
			answer: mustInt("2490570000000000000000"), updatedAt: fresh, want: 2490.57,
		},
		{
			name: "0-decimal feed", symbol: "WBTC", decimals: 0,
			answer: big.NewInt(78400), updatedAt: fresh, want: 78400,
		},
		{
			name: "case-insensitive symbol", symbol: "usdc", decimals: 8,
			answer: big.NewInt(100000000), updatedAt: fresh, want: 1,
		},
		{
			name: "lowercase alias resolves to the same feed", symbol: "cbBTC", decimals: 8,
			answer: big.NewInt(7839987042786), updatedAt: fresh, want: 78399.87042786,
		},
		{
			name: "stale round is rejected, not used", symbol: "ETH", decimals: 8,
			answer: big.NewInt(100000000), updatedAt: staleForETH, wantErr: ErrStale,
		},
		{
			// The bug this guards: a 24h-heartbeat stablecoin feed judged
			// against a one-hour bound is healthy but looks dead, and every
			// USDC basket becomes unroutable.
			name: "a 20h-old round is fine for a 24h-heartbeat feed", symbol: "USDC", decimals: 8,
			answer: big.NewInt(100000000), updatedAt: now.Add(-20 * time.Hour), want: 1,
		},
		{
			name: "past its own heartbeat the stablecoin feed is stale too", symbol: "USDC", decimals: 8,
			answer: big.NewInt(100000000), updatedAt: staleForUSDC, wantErr: ErrStale,
		},
		{
			name: "unknown symbol never gets a default", symbol: "PEPE", decimals: 8,
			answer: big.NewInt(100000000), updatedAt: fresh, wantErr: ErrNoFeed,
		},
		{
			name: "negative answer is rejected", symbol: "USDC", decimals: 8,
			answer: big.NewInt(-100000000), updatedAt: fresh, wantErr: errNotNil,
		},
		{
			name: "zero answer is rejected", symbol: "USDC", decimals: 8,
			answer: big.NewInt(0), updatedAt: fresh, wantErr: errNotNil,
		},
		{
			name: "rpc failure is unavailable, not zero", symbol: "USDC", decimals: 8,
			answer: big.NewInt(100000000), updatedAt: fresh,
			rpcErr: errors.New("over rate limit"), wantErr: errNotNil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rpc := &fakeRPC{
				byData: map[string]string{
					selDecimals:        decimalsWord(tt.decimals),
					selLatestRoundData: roundData(tt.answer, tt.updatedAt),
				},
				err: tt.rpcErr,
			}
			got, err := newTestClient(rpc, now).USD(context.Background(), tt.symbol)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("got price %v, want an error", got.USD)
				}
				if tt.wantErr != errNotNil && !errors.Is(err, tt.wantErr) {
					t.Fatalf("got error %v, want %v", err, tt.wantErr)
				}
				if got.USD != 0 {
					t.Errorf("failed lookup returned %v, want the zero Price", got.USD)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if math.Abs(got.USD-tt.want) > 1e-9 {
				t.Errorf("USD = %v, want %v", got.USD, tt.want)
			}
			if !got.UpdatedAt.Equal(tt.updatedAt.UTC()) {
				t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, tt.updatedAt.UTC())
			}
		})
	}
}

// errNotNil is a sentinel meaning "any error will do".
var errNotNil = errors.New("any error")

func mustInt(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic(s)
	}
	return v
}

// An unknown symbol must not even reach the RPC — there is nothing to ask.
func TestUnknownSymbolMakesNoCall(t *testing.T) {
	rpc := &fakeRPC{byData: map[string]string{}}
	if _, err := newTestClient(rpc, time.Now()).USD(context.Background(), "DOGE"); !errors.Is(err, ErrNoFeed) {
		t.Fatalf("got %v, want ErrNoFeed", err)
	}
	if rpc.calls != 0 {
		t.Errorf("made %d rpc calls for an unknown symbol, want 0", rpc.calls)
	}
}

// One portfolio request touches the same feed repeatedly; the cache keeps that
// to a single pair of calls.
func TestCacheWithinTTL(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	rpc := &fakeRPC{byData: map[string]string{
		selDecimals:        decimalsWord(8),
		selLatestRoundData: roundData(big.NewInt(100000000), now.Add(-time.Minute)),
	}}
	c := newTestClient(rpc, now)
	for i := 0; i < 5; i++ {
		if _, err := c.USD(context.Background(), "USDC"); err != nil {
			t.Fatal(err)
		}
	}
	if rpc.calls != 2 { // decimals + latestRoundData, once
		t.Errorf("made %d rpc calls, want 2", rpc.calls)
	}

	// Past the TTL the price is re-read rather than served forever.
	c.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := c.USD(context.Background(), "USDC"); err != nil {
		t.Fatal(err)
	}
	if rpc.calls != 3 { // decimals stays cached; the round is re-read
		t.Errorf("made %d rpc calls after the TTL, want 3", rpc.calls)
	}
}

func TestDecodeLatestRoundDataRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "0x", "0xdeadbeef", "0x" + word(big.NewInt(1))} {
		if _, _, err := decodeLatestRoundData(raw); err == nil {
			t.Errorf("decoded %q without error", raw)
		}
	}
}

// A negative int256 must read back negative, not wrap to an astronomical price.
func TestToSignedTwosComplement(t *testing.T) {
	neg := big.NewInt(-100000000)
	if got := toSigned(mustInt(new(big.Int).Mod(neg, twoPow256).String())); got.Cmp(neg) != 0 {
		t.Errorf("toSigned round-trip = %v, want %v", got, neg)
	}
}

// The feed table is the one place addresses live; every entry must look like an
// address, so a typo fails here rather than at 3am against a live wallet.
func TestFeedsWellFormed(t *testing.T) {
	for chainID, tbl := range chainTables {
		for symbol, f := range tbl.Feeds {
			if len(f.Address) != 42 || !strings.HasPrefix(f.Address, "0x") {
				t.Errorf("chain %d: %s: %q is not an address", chainID, symbol, f.Address)
			}
			if f.Heartbeat <= 0 {
				t.Errorf("chain %d: %s: heartbeat must be set; measure it onchain", chainID, symbol)
			}
			if symbol != strings.ToUpper(symbol) {
				t.Errorf("chain %d: %s: keys must be upper-case; lookups upper-case the symbol", chainID, symbol)
			}
		}
		// Every chain must at least be able to price the deposit asset.
		if _, ok := tbl.Feeds["USDC"]; !ok {
			t.Errorf("chain %d: missing required feed USDC", chainID)
		}
	}
	for symbol, f := range baseMainnetFeeds {
		if len(f.Address) != 42 || !strings.HasPrefix(f.Address, "0x") {
			t.Errorf("%s: %q is not an address", symbol, f.Address)
		}
		if f.Heartbeat <= 0 {
			t.Errorf("%s: heartbeat must be set; measure it onchain", symbol)
		}
		if symbol != strings.ToUpper(symbol) {
			t.Errorf("%s: keys must be upper-case; lookups upper-case the symbol", symbol)
		}
	}
	for _, required := range []string{"USDC", "ETH", "CBBTC"} {
		if _, ok := baseMainnetFeeds[required]; !ok {
			t.Errorf("missing required feed %s", required)
		}
	}
}

// addrRPC answers per aggregator, so a composed price can be exercised with a
// different round for each leg.
type addrRPC struct {
	byAddr map[string]map[string]string
}

func (a *addrRPC) Call(_ context.Context, to, data string) (string, error) {
	m, ok := a.byAddr[strings.ToLower(to)]
	if !ok {
		return "", fmt.Errorf("no fake feed at %s", to)
	}
	v, ok := m[data]
	if !ok {
		return "", fmt.Errorf("unexpected selector %s", data)
	}
	return v, nil
}

func leg(decimals int64, answer *big.Int, at time.Time) map[string]string {
	return map[string]string{selDecimals: decimalsWord(decimals), selLatestRoundData: roundData(answer, at)}
}

// wstETH and weETH have no USD aggregator on Base, only a ratio against ETH.
// The composite must be ratio x ETH/USD, and must fail if EITHER leg is stale —
// a fresh ETH round does not make a stale ratio safe, and the ETH price alone is
// never an acceptable stand-in for a wrapped staking token.
func TestComposedRatioFeed(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-30 * time.Minute)
	staleRatio := now.Add(-30 * time.Hour) // ratio heartbeat is 24h
	staleETH := now.Add(-3 * time.Hour)    // ETH heartbeat is 1h

	ratioAddr := strings.ToLower(baseMainnetRatios["WSTETH"].Feed.Address)
	ethAddr := strings.ToLower(baseMainnetFeeds["ETH"].Address)
	ratio, _ := new(big.Int).SetString("1243362099355303200", 10) // 1.2433620993553032e18
	ethUSD := big.NewInt(249482000000)                            // 2494.82e8

	tests := []struct {
		name           string
		ratioAt, ethAt time.Time
		want           float64
		wantErr        error
	}{
		{name: "both fresh", ratioAt: fresh, ethAt: fresh, want: 1.2433620993553032 * 2494.82},
		{name: "stale ratio leg", ratioAt: staleRatio, ethAt: fresh, wantErr: ErrStale},
		{name: "stale quote leg", ratioAt: fresh, ethAt: staleETH, wantErr: ErrStale},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(&addrRPC{byAddr: map[string]map[string]string{
				ratioAddr: leg(18, ratio, tc.ratioAt),
				ethAddr:   leg(8, ethUSD, tc.ethAt),
			}}, now)
			got, err := c.USD(context.Background(), "wstETH")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("got err %v, want %v", err, tc.wantErr)
				}
				if got.USD != 0 {
					t.Errorf("stale price returned %v; a stale feed must never yield a value", got.USD)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(got.USD-tc.want) > 1e-6 {
				t.Errorf("got %v, want %v", got.USD, tc.want)
			}
			// The composite is only as fresh as its stalest leg.
			if !got.UpdatedAt.Equal(fresh) {
				t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, fresh)
			}
		})
	}
}

func TestRatioFeedsWellFormed(t *testing.T) {
	for symbol, r := range baseMainnetRatios {
		if len(r.Feed.Address) != 42 || !strings.HasPrefix(r.Feed.Address, "0x") {
			t.Errorf("%s: %q is not an address", symbol, r.Feed.Address)
		}
		if r.Feed.Heartbeat <= 0 {
			t.Errorf("%s: heartbeat must be set; measure it onchain", symbol)
		}
		if symbol != strings.ToUpper(symbol) {
			t.Errorf("%s: keys must be upper-case", symbol)
		}
		if _, ok := baseMainnetFeeds[r.Quote]; !ok {
			t.Errorf("%s: quote %s has no direct feed", symbol, r.Quote)
		}
		if _, dup := baseMainnetFeeds[symbol]; dup {
			t.Errorf("%s: present in both Feeds and RatioFeeds", symbol)
		}
	}
}

// CHAIN_ID must swap the entire table, and an asset with no testnet feed must
// come back unavailable — never proxied onto a mainnet aggregator, never $1.
func TestFeedTableIsChainSelected(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-5 * time.Minute)

	const sepoliaUSDC = "0xd30e2101a97dcbAeBCBC04F14C3f624E67A35165"
	rpc := &addrRPC{byAddr: map[string]map[string]string{
		strings.ToLower(sepoliaUSDC):                      leg(8, big.NewInt(100000000), fresh),
		strings.ToLower(baseMainnetFeeds["USDC"].Address): leg(8, big.NewInt(100000000), fresh),
	}}

	sepolia := New(rpc, ChainBaseSepolia, time.Hour, time.Minute)
	sepolia.now = func() time.Time { return now }
	mainnet := New(rpc, ChainBaseMainnet, time.Hour, time.Minute)
	mainnet.now = func() time.Time { return now }

	if got := sepolia.feeds["USDC"].Address; got != sepoliaUSDC {
		t.Errorf("Base Sepolia USDC feed = %s, want %s", got, sepoliaUSDC)
	}
	if sepolia.feeds["USDC"].Address == mainnet.feeds["USDC"].Address {
		t.Error("both chains resolved to the same aggregator; the table is not chain-selected")
	}
	if p, err := sepolia.USD(context.Background(), "USDC"); err != nil || p.USD != 1 {
		t.Fatalf("Base Sepolia USDC = %v, %v; want 1 with no error", p.USD, err)
	}

	// DAI and wstETH exist on mainnet (direct feed and ratio feed) and have no
	// Base Sepolia aggregator at all. Both must be unavailable there.
	for _, sym := range []string{"DAI", "wstETH", "cbBTC"} {
		p, err := sepolia.USD(context.Background(), sym)
		if !errors.Is(err, ErrNoFeed) {
			t.Errorf("%s on Base Sepolia: err = %v, want ErrNoFeed", sym, err)
		}
		if p.USD != 0 {
			t.Errorf("%s on Base Sepolia returned %v; an unpriceable asset must never yield a value", sym, p.USD)
		}
	}

	// An unknown chain prices nothing rather than falling back to a table.
	unknown := New(rpc, 1234567, time.Hour, time.Minute)
	if _, err := unknown.USD(context.Background(), "USDC"); !errors.Is(err, ErrNoFeed) {
		t.Errorf("unknown chain: err = %v, want ErrNoFeed", err)
	}
}
