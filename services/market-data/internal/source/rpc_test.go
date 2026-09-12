package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
	"github.com/snehendu098/sweem-basket/internal/shared/prices"
)

// fakeRPC answers by (to, data). No network, ever. A missing entry reverts,
// which is exactly what a bad call does on chain.
type fakeRPC struct {
	ret map[string]string
}

func (f *fakeRPC) Call(_ context.Context, to, data string) (string, error) {
	if v, ok := f.ret[strings.ToLower(to)+strings.ToLower(data)]; ok {
		return v, nil
	}
	return "", errors.New("execution reverted")
}

// usdFeed reuses the package's stubFeed; usd <= 0 means "no price at all",
// which must drop the venue rather than value it at zero.
func usdFeed(usd float64) stubFeed {
	if usd <= 0 {
		return stubFeed{}
	}
	return stubFeed{usd: map[string]float64{"USDC": usd, "WETH": usd}}
}

func word(v *big.Int) string {
	h := v.Text(16)
	return strings.Repeat("0", 64-len(h)) + h
}

func wordN(n int64) string { return word(big.NewInt(n)) }

func addrWord(a string) string {
	return strings.Repeat("0", 24) + strings.ToLower(strings.TrimPrefix(a, "0x"))
}

// abiString encodes a solidity string return.
func abiString(s string) string {
	b := []byte(s)
	padded := append(append([]byte{}, b...), make([]byte, 32-len(b)%32)...)[:((len(b)/32)+1)*32]
	return "0x" + wordN(32) + wordN(int64(len(b))) + fmt.Sprintf("%x", padded)
}

// aaveConfig builds a reserve configuration bitmap.
func aaveConfig(decimals int64, active, frozen, paused bool) *big.Int {
	c := new(big.Int).Lsh(big.NewInt(decimals), bitDecimals)
	for bit, on := range map[uint]bool{bitActive: active, bitFrozen: frozen, bitPaused: paused} {
		if on {
			c.SetBit(c, int(bit), 1)
		}
	}
	return c
}

// reserveData builds the 15-word getReserveData return.
func reserveData(cfg, rate *big.Int, aToken string) string {
	out := "0x" + word(cfg) + wordN(0) + word(rate)
	for i := 3; i < aaveReserveDataWords; i++ {
		switch i {
		case wordAToken:
			out += addrWord(aToken)
		default:
			out += wordN(0)
		}
	}
	return out
}

// addressList builds an address[] return.
func addressList(addrs ...string) string {
	out := "0x" + wordN(32) + wordN(int64(len(addrs)))
	for _, a := range addrs {
		out += addrWord(a)
	}
	return out
}

const (
	aUSDCBase = "0x4e65fe4dba92790696d040ac24aa414708f5c0ab"
	poolBase  = "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5"
	cometBase = "0xb125E6687d4313864e53df431d5425969c15Eb2F"
)

var baseChain = Chain{Label: chains.LabelBaseMainnet, ID: chains.BaseMainnet}

// wethLiquidityRate is a real Base reading: 1.7234...% APR as a ray.
var wethLiquidityRate, _ = new(big.Int).SetString("17234812126058827297172826", 10)

func newTestRPCSource(t *testing.T, ret map[string]string, usd float64) *RPCSource {
	t.Helper()
	f := Filter{Chains: []string{baseChain.Label}, MaxAPY: 1000, AllowZeroAPY: true}
	s, err := NewRPC(baseChain, &fakeRPC{ret: ret}, f, usdFeed(usd))
	if err != nil {
		t.Fatal(err)
	}
	s.Attempts = 1 // a test never waits on backoff
	return s
}

// aaveReturns is a one-reserve Pool: USDC, active, 1M supplied.
func aaveReturns(cfg, rate *big.Int) map[string]string {
	return map[string]string{
		strings.ToLower(poolBase) + SelGetReservesList:                                       addressList(usdcBase),
		strings.ToLower(poolBase) + strings.ToLower(AddressArg(SelGetReserveData, usdcBase)): reserveData(cfg, rate, aUSDCBase),
		aUSDCBase + SelTotalSupply:                                                           "0x" + wordN(1_000_000_000_000), // 1e6 USDC at 6 decimals
		strings.ToLower(usdcBase) + strings.ToLower(AddressArg(SelBalanceOf, aUSDCBase)):     "0x" + wordN(900_000_000_000),   // 900k USDC withdrawable
	}
}

func TestAaveRayRateToAPY(t *testing.T) {
	// Hand-computed: APR = 17234812126058827297172826 / 1e27, compounded every
	// second for a 365-day year.
	apr := 0.017234812126058827
	want := math.Expm1(SecondsPerYear*math.Log1p(apr/SecondsPerYear)) * 100

	s := newTestRPCSource(t, aaveReturns(aaveConfig(6, true, false, false), wethLiquidityRate), 1)
	venues, err := s.aave(context.Background(), NewCaller(&fakeRPC{ret: aaveReturns(aaveConfig(6, true, false, false), wethLiquidityRate)}), NewPricer(context.Background(), usdFeed(1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(venues) != 1 {
		t.Fatalf("got %d venues, want 1", len(venues))
	}
	got := venues[0]
	if math.Abs(got.APY-want) > 1e-9 {
		t.Errorf("APY = %v, want %v", got.APY, want)
	}
	// The two sources must agree bit for bit on the same input, or the
	// disagreement log fires on every cycle for no reason.
	if sub := RayRateToAPY(wethLiquidityRate); got.APY != sub {
		t.Errorf("rpc APY %v != subgraph APY %v for the same ray", got.APY, sub)
	}
	if math.Abs(got.TVLUsd-1_000_000) > 1e-6 {
		t.Errorf("TVL = %v, want 1000000", got.TVLUsd)
	}
	_ = s
}

func TestAaveReserveFlagsExcludeVenues(t *testing.T) {
	tests := []struct {
		name                   string
		active, frozen, paused bool
		want                   int
	}{
		{"active", true, false, false, 1},
		{"frozen", true, true, false, 0},
		{"paused", true, false, true, 0},
		{"inactive", false, false, false, 0},
		{"frozen and paused", true, true, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ret := aaveReturns(aaveConfig(6, tt.active, tt.frozen, tt.paused), wethLiquidityRate)
			s := newTestRPCSource(t, ret, 1)
			venues, err := s.aave(context.Background(), NewCaller(&fakeRPC{ret: ret}), NewPricer(context.Background(), usdFeed(1)))
			if err != nil {
				t.Fatal(err)
			}
			if len(venues) != tt.want {
				t.Fatalf("got %d venues, want %d", len(venues), tt.want)
			}
		})
	}
}

// A call that reverts must omit the venue. A zero rate would read as "this
// market pays nothing", which routes money in the opposite direction.
func TestRevertingCallOmitsRatherThanZeroes(t *testing.T) {
	t.Run("aave reserve data reverts", func(t *testing.T) {
		ret := aaveReturns(aaveConfig(6, true, false, false), wethLiquidityRate)
		delete(ret, strings.ToLower(poolBase)+strings.ToLower(AddressArg(SelGetReserveData, usdcBase)))
		s := newTestRPCSource(t, ret, 1)
		venues, err := s.aave(context.Background(), NewCaller(&fakeRPC{ret: ret}), NewPricer(context.Background(), usdFeed(1)))
		if err != nil {
			t.Fatal(err)
		}
		if len(venues) != 0 {
			t.Fatalf("got %d venues with a reverting call, want 0 (got APY %v)", len(venues), venues[0].APY)
		}
		_ = s
	})
	t.Run("aave pool reverts", func(t *testing.T) {
		s := newTestRPCSource(t, map[string]string{}, 1)
		if _, err := s.aave(context.Background(), NewCaller(&fakeRPC{ret: map[string]string{}}), NewPricer(context.Background(), usdFeed(1))); err == nil {
			t.Fatal("an unreadable Pool must be an error, not an empty venue list")
		}
	})
	t.Run("comet supply rate reverts", func(t *testing.T) {
		ret := cometReturns(1_559_764_904)
		delete(ret, strings.ToLower(cometBase)+strings.ToLower(UintArg(SelGetSupplyRate, big.NewInt(905253049459064200))))
		venues, err := newTestRPCSource(t, ret, 1).compound(context.Background(), NewCaller(&fakeRPC{ret: ret}), NewPricer(context.Background(), usdFeed(1)))
		if err != nil {
			t.Fatal(err)
		}
		if len(venues) != 0 {
			t.Fatalf("got %d venues with a reverting call, want 0", len(venues))
		}
	})
	t.Run("unpriceable asset", func(t *testing.T) {
		ret := aaveReturns(aaveConfig(6, true, false, false), wethLiquidityRate)
		s := newTestRPCSource(t, ret, 0) // feed has no price
		venues, err := s.aave(context.Background(), NewCaller(&fakeRPC{ret: ret}), NewPricer(context.Background(), usdFeed(0)))
		if err != nil {
			t.Fatal(err)
		}
		if len(venues) != 0 {
			t.Fatalf("got %d venues for an unpriceable asset, want 0", len(venues))
		}
		_ = s
	})
}

// cometReturns is a one-market Comet at 90.5% utilization.
func cometReturns(perSecond int64) map[string]string {
	util := big.NewInt(905253049459064200)
	return map[string]string{
		strings.ToLower(cometBase) + SelSymbol:                                           abiString("cUSDCv3"),
		strings.ToLower(cometBase) + SelBaseToken:                                        "0x" + addrWord(usdcBase),
		strings.ToLower(cometBase) + SelDecimals:                                         "0x" + wordN(6),
		strings.ToLower(cometBase) + SelTotalSupply:                                      "0x" + wordN(1_000_000_000_000),
		strings.ToLower(cometBase) + SelGetUtilization:                                   "0x" + word(util),
		strings.ToLower(cometBase) + strings.ToLower(UintArg(SelGetSupplyRate, util)):    "0x" + wordN(perSecond),
		strings.ToLower(usdcBase) + strings.ToLower(AddressArg(SelBalanceOf, cometBase)): "0x" + wordN(95_000_000_000),
	}
}

func TestCometPerSecondToAPY(t *testing.T) {
	// A real Base cUSDCv3 reading: 1.559764904e-9 per second scaled 1e18.
	const perSecond = 1_559_764_904
	r := float64(perSecond) / 1e18
	want := math.Expm1(SecondsPerYear*math.Log1p(r)) * 100

	ret := cometReturns(perSecond)
	venues, err := newTestRPCSource(t, ret, 1).compound(context.Background(), NewCaller(&fakeRPC{ret: ret}), NewPricer(context.Background(), usdFeed(1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(venues) != 1 {
		t.Fatalf("got %d venues, want 1", len(venues))
	}
	if got := venues[0].APY; math.Abs(got-want) > 1e-9 {
		t.Errorf("APY = %v, want %v", got, want)
	}
	// The subgraph reports the same market as a decimal APR. Per-second rate x
	// seconds-per-year is that APR, and both paths must land on one number —
	// this is the assertion that would have caught a per-block conversion.
	if sub := APRToAPY(r * SecondsPerYear); math.Abs(venues[0].APY-sub) > 1e-9 {
		t.Errorf("rpc APY %v != subgraph APY %v for the same rate", venues[0].APY, sub)
	}
}

// A Comet whose symbol() does not match the table is a wrong address, and a
// wrong address is a rate for a market nobody is in.
func TestCometSymbolMismatchIsRejected(t *testing.T) {
	ret := cometReturns(1_559_764_904)
	ret[strings.ToLower(cometBase)+SelSymbol] = abiString("cWETHv3")
	venues, err := newTestRPCSource(t, ret, 1).compound(context.Background(), NewCaller(&fakeRPC{ret: ret}), NewPricer(context.Background(), usdFeed(1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(venues) != 0 {
		t.Fatalf("got %d venues for a symbol mismatch, want 0", len(venues))
	}
}

// The venue id is the routing key the executor allowlist matches on. If the two
// sources spell it differently the allowlist matches one of them and the other
// is dead weight, silently.
func TestVenueIDMatchesSubgraphSource(t *testing.T) {
	ctx := context.Background()

	ret := aaveReturns(aaveConfig(6, true, false, false), wethLiquidityRate)
	live, err := newTestRPCSource(t, ret, 1).aave(ctx, NewCaller(&fakeRPC{ret: ret}), NewPricer(ctx, usdFeed(1)))
	if err != nil || len(live) != 1 {
		t.Fatalf("aave rpc read: %v (%d venues)", err, len(live))
	}
	sub, err := NewAaveV3(baseChain.Label, "x").Map(NewPricer(ctx, usdFeed(1)), json.RawMessage(fmt.Sprintf(
		`{"reserves":[{"id":%q,"symbol":"USDC","decimals":6,"underlyingAsset":%q,"liquidityRate":%q,"totalLiquidity":"1000000000000","isActive":true,"price":{"priceInEth":"100000000","oracle":{"baseCurrencyUnit":"100000000"}}}]}`,
		usdcBase, usdcBase, wethLiquidityRate.String())))
	if err != nil || len(sub) != 1 {
		t.Fatalf("aave subgraph map: %v (%d venues)", err, len(sub))
	}
	if live[0].ID != sub[0].ID {
		t.Errorf("aave id: rpc %q != subgraph %q", live[0].ID, sub[0].ID)
	}

	cret := cometReturns(1_559_764_904)
	cLive, err := newTestRPCSource(t, cret, 1).compound(ctx, NewCaller(&fakeRPC{ret: cret}), NewPricer(ctx, usdFeed(1)))
	if err != nil || len(cLive) != 1 {
		t.Fatalf("comet rpc read: %v (%d venues)", err, len(cLive))
	}
	cSub, err := NewCompoundV3(baseChain.Label, "x").Map(nil, json.RawMessage(fmt.Sprintf(
		`{"markets":[{"id":%q,"configuration":{"symbol":"cUSDCv3","baseToken":{"token":{"address":%q,"symbol":"USDC"}}},"accounting":{"totalBaseSupplyUsd":"1000000","supplyApr":"0.0491","netSupplyApr":"0.0491"}}]}`,
		strings.ToLower(cometBase), usdcBase)))
	if err != nil || len(cSub) != 1 {
		t.Fatalf("comet subgraph map: %v (%d venues)", err, len(cSub))
	}
	if cLive[0].ID != cSub[0].ID {
		t.Errorf("compound id: rpc %q != subgraph %q", cLive[0].ID, cSub[0].ID)
	}
}

// Every hard-coded address must look like one, so a typo fails here rather than
// as a silently missing protocol at 3am.
func TestRPCAddressTablesWellFormed(t *testing.T) {
	for chainID, pool := range aavePools {
		if len(pool) != 42 || !strings.HasPrefix(pool, "0x") {
			t.Errorf("chain %d: aave pool %q is not an address", chainID, pool)
		}
	}
	for chainID, ms := range comets {
		for _, m := range ms {
			if len(m.Address) != 42 || !strings.HasPrefix(m.Address, "0x") {
				t.Errorf("chain %d: comet %q is not an address", chainID, m.Address)
			}
			if !strings.HasPrefix(m.Symbol, "c") || !strings.HasSuffix(m.Symbol, "v3") {
				t.Errorf("chain %d: %q is not a Comet symbol", chainID, m.Symbol)
			}
		}
	}
	for _, id := range chains.Supported() {
		if aavePools[id] == "" && len(comets[id]) == 0 {
			t.Errorf("chain %d has no live rate coverage at all", id)
		}
	}
}

// batchRPC is a fakeRPC that also answers batches, so the production path (one
// POST for fifteen reserves) is exercised rather than only the fallback.
type batchRPC struct {
	fakeRPC
	batches, singles int
}

func (b *batchRPC) Call(ctx context.Context, to, data string) (string, error) {
	b.singles++
	return b.fakeRPC.Call(ctx, to, data)
}

func (b *batchRPC) CallBatch(ctx context.Context, calls []prices.Call) ([]prices.Result, error) {
	b.batches++
	out := make([]prices.Result, len(calls))
	for i, c := range calls {
		raw, err := b.fakeRPC.Call(ctx, c.To, c.Data)
		out[i] = prices.Result{Raw: raw, Err: err}
	}
	return out, nil
}

// Batching must change the number of round trips and nothing else. A node that
// cannot batch still answers one call at a time — that fallback is what every
// other test in this file exercises.
func TestBatchedReadsMatchSingleCalls(t *testing.T) {
	ret := aaveReturns(aaveConfig(6, true, false, false), wethLiquidityRate)
	ctx := context.Background()

	single, err := newTestRPCSource(t, ret, 1).aave(ctx, NewCaller(&fakeRPC{ret: ret}), NewPricer(ctx, usdFeed(1)))
	if err != nil {
		t.Fatal(err)
	}
	b := &batchRPC{fakeRPC: fakeRPC{ret: ret}}
	batched, err := newTestRPCSource(t, ret, 1).aave(ctx, NewCaller(b), NewPricer(ctx, usdFeed(1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(single) != 1 || len(batched) != 1 {
		t.Fatalf("got %d single and %d batched venues, want 1 each", len(single), len(batched))
	}
	if single[0].APY != batched[0].APY || single[0].TVLUsd != batched[0].TVLUsd || single[0].ID != batched[0].ID {
		t.Errorf("batched venue %+v differs from single-call venue %+v", batched[0], single[0])
	}
	if b.batches == 0 {
		t.Error("a batching transport was never asked for a batch")
	}
	// getReservesList is the only call the batch path cannot fold in: it is
	// what produces the list to batch over.
	if b.singles > 1 {
		t.Errorf("made %d single calls alongside %d batches, want 1", b.singles, b.batches)
	}
}

// --- intrinsic yield ---

const (
	wstETHBase   = "0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452"
	wstETHOracle = "0xB88BAc61a4Ca37C43a3725912B1f472c9A5bc061"
)

// roundReturn builds a latestRoundData/getRoundData return.
func roundReturn(roundID, answer *big.Int, at time.Time) string {
	return "0x" + word(roundID) + word(answer) + wordN(at.Unix()) + wordN(at.Unix()) + word(roundID)
}

// rateFeedReturns is one wstETH exchange-rate feed whose rate rose by `growth`
// over `window`, plus the token's supply reads.
func rateFeedReturns(now time.Time, window time.Duration, before, after *big.Int) map[string]string {
	latestID := testRoundID()
	ret := map[string]string{
		strings.ToLower(wstETHOracle) + SelDescription:     abiString("wstETH-stETH Exchange Rate"),
		strings.ToLower(wstETHOracle) + SelLatestRoundData: roundReturn(latestID, after, now),
		wstETHBase + SelDecimals:                           "0x" + wordN(18),
		wstETHBase + SelTotalSupply:                        "0x" + word(mustBig("100000000000000000000000")), // 100k wstETH
	}
	// Only the widest offset carries the older reading; the nearer rounds sit
	// at the same value a minute apart, which is what these feeds really do.
	for _, k := range roundOffsets {
		id := new(big.Int).Sub(latestID, big.NewInt(k))
		at, value := now.Add(-time.Minute), after
		if k == roundOffsets[len(roundOffsets)-1] {
			at, value = now.Add(-window), before
		}
		ret[strings.ToLower(wstETHOracle)+strings.ToLower(UintArg(SelGetRoundData, id))] = roundReturn(id, value, at)
	}
	return ret
}

// testRoundID is a proxy round id: phase 2 in the high bits, aggregator round
// 228 in the low 64. Walking back must stay inside the phase.
func testRoundID() *big.Int {
	return new(big.Int).Or(new(big.Int).Lsh(big.NewInt(2), 64), big.NewInt(228))
}

func mustBig(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic(s)
	}
	return v
}

func TestExchangeRateGrowthToAPY(t *testing.T) {
	now := time.Now().UTC()
	const window = 7 * 24 * time.Hour
	before, after := mustBig("1243254206102366400"), mustBig("1243869391109877300")

	ret := rateFeedReturns(now, window, before, after)
	venues, err := newTestRPCSource(t, ret, 4000).intrinsic(
		context.Background(), NewCaller(&fakeRPC{ret: ret}), NewPricer(context.Background(), stubFeed{usd: map[string]float64{"WSTETH": 4000}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(venues) != 1 {
		t.Fatalf("got %d venues, want 1 (wstETH)", len(venues))
	}
	got := venues[0]

	// The subgraph path derives the identical number from the identical pair,
	// so the two sources must not disagree about the same drift.
	want, ok := AnnualizeGrowth("t", []GrowthSample{
		{At: now, Value: after}, {At: now.Add(-window), Value: before},
	}, DefaultHoldMinWindow, DefaultHoldMaxWindow)
	if !ok {
		t.Fatal("the reference computation refused the samples")
	}
	if math.Abs(got.APY-want) > 1e-9 {
		t.Errorf("APY = %v, want %v", got.APY, want)
	}
	if got.APYIntrinsic != got.APY || got.APYBase != 0 {
		t.Errorf("a hold venue's yield is all intrinsic: got APY %v base %v intrinsic %v", got.APY, got.APYBase, got.APYIntrinsic)
	}
	if got.Project != ProtocolHold || got.PoolID != wstETHBase {
		t.Errorf("got %s venue with pool %s, want a hold venue on the token itself", got.Project, got.PoolID)
	}
}

func TestIntrinsicOmissions(t *testing.T) {
	now := time.Now().UTC()
	flat := mustBig("1243869391109877300")

	tests := []struct {
		name string
		mut  func(map[string]string)
	}{
		{
			// The whole point of the adapter: a feed that is not moving is a
			// stale oracle, not an asset that yields nothing.
			name: "a flat exchange rate is unavailable, not 0%",
			mut:  func(m map[string]string) {},
		},
		{
			// Reading a market price as an exchange rate is how a discount
			// becomes invented yield; the description is what prevents it.
			name: "a provider that describes itself as something else",
			mut: func(m map[string]string) {
				m[strings.ToLower(wstETHOracle)+SelDescription] = abiString("wstETH / USD")
			},
		},
		{
			name: "the whole feed reverts",
			mut: func(m map[string]string) {
				delete(m, strings.ToLower(wstETHOracle)+SelLatestRoundData)
			},
		},
		{
			name: "the rate fell",
			mut: func(m map[string]string) {
				id := new(big.Int).Sub(testRoundID(), big.NewInt(roundOffsets[len(roundOffsets)-1]))
				m[strings.ToLower(wstETHOracle)+strings.ToLower(UintArg(SelGetRoundData, id))] =
					roundReturn(id, mustBig("1300000000000000000"), now.Add(-7*24*time.Hour))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ret := rateFeedReturns(now, 7*24*time.Hour, flat, flat)
			tt.mut(ret)
			venues, err := newTestRPCSource(t, ret, 4000).intrinsic(
				context.Background(), NewCaller(&fakeRPC{ret: ret}), NewPricer(context.Background(), stubFeed{usd: map[string]float64{"WSTETH": 4000}}))
			if err != nil {
				t.Fatal(err)
			}
			if len(venues) != 0 {
				t.Fatalf("got a venue paying %v%%, want it omitted", venues[0].APY)
			}
		})
	}
}

// Sky publishes the savings rate itself, so there is no history to sample. The
// live reading of ssr is 1.0000000011214849 per second.
func TestSkySSRToAPY(t *testing.T) {
	ssr := mustBig("1000000001121484904942349200")
	want := 3.6000004252923747 // hand-computed from that ssr

	if got := RayPerSecondFactorToAPY(ssr); math.Abs(got-want) > 1e-9 {
		t.Errorf("APY = %v, want %v", got, want)
	}
	// A factor at or below 1.0 is no yield, and must not come back as a rate.
	for _, flat := range []string{"1000000000000000000000000000", "999999999999999999999999999"} {
		if got := RayPerSecondFactorToAPY(mustBig(flat)); got != 0 {
			t.Errorf("ssr %s gave APY %v, want 0 so the caller omits the venue", flat, got)
		}
	}
}

// Every rate feed must name a token market-data can actually identify, or the
// venue is measured and then dropped for want of an asset.
func TestRateFeedTableWellFormed(t *testing.T) {
	for chainID, feeds := range rateFeeds {
		for _, f := range feeds {
			if len(f.Token) != 42 || len(f.Provider) != 42 {
				t.Errorf("chain %d: %q/%q is not an address pair", chainID, f.Token, f.Provider)
			}
			if f.Token == f.Provider {
				t.Errorf("chain %d: %s is its own rate provider", chainID, f.Token)
			}
			if a := ResolveAsset([]string{f.Token}, ""); a == "" {
				t.Errorf("chain %d: token %s resolves to no canonical asset", chainID, f.Token)
			}
			switch f.Kind {
			case kindExchangeRate:
				if f.Description == "" {
					t.Errorf("chain %d: %s has no description() to verify against", chainID, f.Token)
				}
			case kindSkySSR:
			default:
				t.Errorf("chain %d: unknown rate feed kind %q", chainID, f.Kind)
			}
		}
	}
}
