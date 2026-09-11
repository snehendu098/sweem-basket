package gas

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

type fakeGas struct {
	wei   *big.Int
	err   error
	calls int
}

func (f *fakeGas) GasPriceWei(context.Context) (*big.Int, error) {
	f.calls++
	return f.wei, f.err
}

type fakeFeed struct {
	price     float64
	updatedAt time.Time
	err       error
	calls     int
}

func (f *fakeFeed) ETHUSD(context.Context) (float64, time.Time, error) {
	f.calls++
	return f.price, f.updatedAt, f.err
}

// 500,000 gas * 20 gwei = 0.01 ETH; at $2,500/ETH that is exactly $25.
func TestCostUSD_500kGasAt20GweiAnd2500UsdPerEth_Is25Dollars(t *testing.T) {
	gwei20 := big.NewInt(20_000_000_000)
	if got := CostUSD(500_000, gwei20, 2500); math.Abs(got-25) > 1e-9 {
		t.Fatalf("got %v want 25", got)
	}
}

func TestCostUSDTable(t *testing.T) {
	tests := []struct {
		name        string
		units       uint64
		gasPriceWei int64
		ethUSD      float64
		want        float64
	}{
		// Base is cheap: 0.0094 gwei is a real mainnet reading (block ~51.09M).
		// 556,000 gas * 9,446,971 wei = 5.252515876e12 wei = 5.252515876e-6 ETH;
		// at $2478.00208554/ETH that is $0.013015745.
		{"556k gas at 0.00945 gwei and $2478/ETH", UnitsRebalance, 9_446_971, 2478.00208554, 0.013015745},
		{"1 gas at 1 wei is 1e-18 ETH", 1, 1, 2500, 2.5e-15},
		{"zero units is free", 0, 20_000_000_000, 2500, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CostUSD(tt.units, big.NewInt(tt.gasPriceWei), tt.ethUSD)
			if math.Abs(got-tt.want) > math.Max(1e-15, tt.want*1e-4) {
				t.Fatalf("got %.12g want %.12g", got, tt.want)
			}
		})
	}
}

func TestEstimatorCostUSD(t *testing.T) {
	tests := []struct {
		name    string
		gas     *fakeGas
		feed    *fakeFeed
		maxAge  time.Duration
		want    float64
		wantErr bool
	}{
		{
			name: "live inputs",
			gas:  &fakeGas{wei: big.NewInt(20_000_000_000)},
			feed: &fakeFeed{price: 2500, updatedAt: now.Add(-time.Minute)},
			want: 25,
		},
		{
			name:    "gas price unavailable",
			gas:     &fakeGas{err: errors.New("node down")},
			feed:    &fakeFeed{price: 2500, updatedAt: now},
			wantErr: true,
		},
		{
			name:    "eth price unavailable",
			gas:     &fakeGas{wei: big.NewInt(1)},
			feed:    &fakeFeed{err: errors.New("call reverted")},
			wantErr: true,
		},
		{
			name:    "stale oracle answer is refused",
			gas:     &fakeGas{wei: big.NewInt(20_000_000_000)},
			feed:    &fakeFeed{price: 2500, updatedAt: now.Add(-2 * time.Hour)},
			maxAge:  time.Hour,
			wantErr: true,
		},
		{
			name:   "answer just inside the age limit is used",
			gas:    &fakeGas{wei: big.NewInt(20_000_000_000)},
			feed:   &fakeFeed{price: 2500, updatedAt: now.Add(-59 * time.Minute)},
			maxAge: time.Hour,
			want:   25,
		},
		{
			name:    "non-positive gas price is refused",
			gas:     &fakeGas{wei: big.NewInt(0)},
			feed:    &fakeFeed{price: 2500, updatedAt: now},
			wantErr: true,
		},
		{
			name:    "non-positive price is refused",
			gas:     &fakeGas{wei: big.NewInt(1)},
			feed:    &fakeFeed{price: 0, updatedAt: now},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Estimator{Gas: tt.gas, Feed: tt.feed, Units: 500_000,
				MaxAge: tt.maxAge, Now: func() time.Time { return now }}
			got, err := e.CostUSD(context.Background())
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				// The caller keys its skip off this sentinel.
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("error %v does not wrap ErrUnavailable", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestEstimatorCachesPriceNotGasPrice(t *testing.T) {
	g := &fakeGas{wei: big.NewInt(20_000_000_000)}
	f := &fakeFeed{price: 2500, updatedAt: now}
	e := &Estimator{Gas: g, Feed: f, Units: 500_000, CacheTTL: time.Minute,
		Now: func() time.Time { return now }}

	for range 3 {
		if _, err := e.CostUSD(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls != 1 {
		t.Fatalf("price fetched %d times, want 1", f.calls)
	}
	// Gas price is per call — the caller makes one per pass.
	if g.calls != 3 {
		t.Fatalf("gas price fetched %d times, want 3", g.calls)
	}
}

func TestEstimatorOverrideBypassesEverything(t *testing.T) {
	e := &Estimator{
		Gas: &fakeGas{err: errors.New("node down")}, Feed: &fakeFeed{err: errors.New("no")},
		Override: 1.25, Now: func() time.Time { return now },
	}
	got, err := e.CostUSD(context.Background())
	if err != nil || got != 1.25 {
		t.Fatalf("got %v %v", got, err)
	}
}

// --- chainlink decoding ---

type fakeCaller struct {
	ret map[string]string // selector -> hex return data
	err error
}

func (f fakeCaller) Call(_ context.Context, _, data string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.ret[data]
	if !ok {
		return nil, errors.New("unexpected selector " + data)
	}
	return hex.DecodeString(v)
}

func word(hexNoPrefix string) string {
	return strings.Repeat("0", 64-len(hexNoPrefix)) + hexNoPrefix
}

func TestChainlinkETHUSD(t *testing.T) {
	// A real reading from the Base aggregator: answer 247800208554 at 8
	// decimals = $2478.00208554.
	round := word("1") + word("39b20b1caa") + word("6aa17ac7") + word("6aa17ad5") + word("1")
	c := &Chainlink{Address: ChainlinkETHUSDBase, Caller: fakeCaller{ret: map[string]string{
		selDecimals:        word("8"),
		selLatestRoundData: round,
	}}}

	price, updatedAt, err := c.ETHUSD(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(price-2478.00208554) > 1e-6 {
		t.Fatalf("price %v", price)
	}
	if updatedAt.Unix() != 0x6aa17ad5 {
		t.Fatalf("updatedAt %v", updatedAt)
	}
}

// decimals() is read, not assumed: the same answer at 18 decimals is a
// completely different price.
func TestChainlinkHonoursDecimals(t *testing.T) {
	round := word("1") + word("38d7ea4c68000") + word("0") + word("6aa17ad5") + word("1")
	c := &Chainlink{Address: ChainlinkETHUSDBase, Caller: fakeCaller{ret: map[string]string{
		selDecimals:        word("12"), // 18 decimals
		selLatestRoundData: round,
	}}}
	price, _, err := c.ETHUSD(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(price-0.001) > 1e-12 {
		t.Fatalf("price %v want 0.001", price)
	}
}

func TestChainlinkRejectsBadAnswers(t *testing.T) {
	neg := word("1") + strings.Repeat("f", 64) + word("0") + word("6aa17ad5") + word("1")
	tests := []struct {
		name string
		ret  map[string]string
	}{
		{"negative price", map[string]string{selDecimals: word("8"), selLatestRoundData: neg}},
		{"short response", map[string]string{selDecimals: word("8"), selLatestRoundData: word("1")}},
		{"implausible decimals", map[string]string{selDecimals: word("64")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Chainlink{Address: ChainlinkETHUSDBase, Caller: fakeCaller{ret: tt.ret}}
			if _, _, err := c.ETHUSD(context.Background()); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestChainlinkCachesDecimals(t *testing.T) {
	calls := 0
	c := &Chainlink{Address: ChainlinkETHUSDBase, Caller: countingCaller{&calls, fakeCaller{ret: map[string]string{
		selDecimals:        word("8"),
		selLatestRoundData: word("1") + word("39b20b1caa") + word("0") + word("6aa17ad5") + word("1"),
	}}}}
	for range 3 {
		if _, _, err := c.ETHUSD(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 4 { // 1 decimals + 3 rounds
		t.Fatalf("calls %d want 4", calls)
	}
}

type countingCaller struct {
	n     *int
	inner fakeCaller
}

func (c countingCaller) Call(ctx context.Context, to, data string) ([]byte, error) {
	*c.n++
	return c.inner.Call(ctx, to, data)
}
