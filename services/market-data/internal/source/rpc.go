package source

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
	"github.com/snehendu098/sweem-basket/internal/shared/config"
	"github.com/snehendu098/sweem-basket/internal/shared/prices"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

// RPCSource reads Aave V3 and Compound III supply rates straight off the chain.
//
// Why a second source exists at all: a subgraph must replay history before it
// can answer, and a freshly deployed mainnet subgraph is days from current —
// while the same numbers are one eth_call away, live, with no sync. So the
// split is by what the protocol publishes, not by preference:
//
//	spot rates     eth_call   Aave, Compound   instant, no sync
//	derived rates  subgraph   Morpho, LSTs     needs a time series
//
// Morpho and the liquid-staking tokens expose no rate field at all, only a
// drifting share price, so their APY genuinely has to come from historical
// samples. Those stay on GraphSource. See Reconcile for how the two merge.
//
// Nothing here ever invents a rate. A reverting call, a rate-limited node, an
// unpriceable asset or a frozen reserve all omit the venue with a logged
// reason — never a zero rate, which would read as "this market pays nothing"
// and route money away from a market that pays plenty.
type RPCSource struct {
	Chain  Chain
	Filter Filter
	Prices PriceFeed

	rpc    prices.RPC
	pool   string        // Aave V3 Pool for this chain; "" when unsupported
	comets []cometMarket // Comet markets for this chain
	// Attempts bounds the per-call retry. Public Base endpoints answer a burst
	// of eth_calls with 429, and retrying into one earns a ban, so a busy node
	// costs us a cycle rather than access.
	Attempts int
	// RetryPause and MinInterval shape how hard this source leans on a public
	// node: see Caller. Both are fields so a deployment with a private node can
	// turn the pacing off.
	RetryPause  time.Duration
	MinInterval time.Duration
	MaxBatch    int
	// MinWindow and MaxWindow bound the exchange-rate sampling; see
	// AnnualizeGrowth. Same defaults as the subgraph hold adapter, so the two
	// sources cannot disagree about which samples are trustworthy.
	MinWindow time.Duration
	MaxWindow time.Duration

	mu     sync.RWMutex
	status map[string]Status
}

// aavePools are the V3 Pool addresses, each confirmed by calling
// getReservesList() on it. A chain absent here has no Aave venues from this
// source — it does not get a guessed address.
var aavePools = map[int]string{
	chains.BaseMainnet: "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5",
	chains.BaseSepolia: "0x07eA79F68B2B3df564D0A34F8e19D9B1e339814b",
}

// cometMarket is one Compound III market. Symbol is what the contract must
// report: the address list is a candidate list, and symbol()/baseToken() are
// what turn a candidate into a venue. A mismatch is a wrong address, and a
// wrong address is a rate for a market our users are not in.
type cometMarket struct {
	Address string
	Symbol  string
}

var comets = map[int][]cometMarket{
	chains.BaseMainnet: {
		{"0xb125E6687d4313864e53df431d5425969c15Eb2F", "cUSDCv3"},
		{"0x46e6b214b524310239732D51387075E0e70970bf", "cWETHv3"},
		{"0x2c776041ccfe903071af44aa147368a9c8eea518", "cUSDSv3"},
		{"0x784efeb622244d2348d4f2522f8860b96fbece89", "cAEROv3"},
		{"0x9c4ec768c28520b50860ea7a15bd7213a9ff58bf", "cUSDbCv3"},
	},
	chains.BaseSepolia: {
		{"0x571621Ce60Cebb0c1D442B5afb38B1663C6Bf017", "cUSDCv3"},
		{"0x61490650AbaA31393464C3f34E8B29cd1C44118E", "cWETHv3"},
	},
}

// NewRPC builds the direct-read source for one chain. Like NewGraph it refuses
// to run without a price feed: an unvalued venue misroutes money exactly as
// badly as a mispriced one.
func NewRPC(c Chain, rpc prices.RPC, f Filter, feed PriceFeed) (*RPCSource, error) {
	if rpc == nil {
		return nil, errors.New("rpc transport is required for the direct rate source")
	}
	if feed == nil {
		return nil, errors.New("price feed is required: venue TVL cannot be valued without it")
	}
	s := &RPCSource{
		Chain: c, Filter: f, Prices: feed, rpc: rpc,
		pool: aavePools[c.ID], comets: comets[c.ID],
		// Measured against mainnet.base.org: ~60 eth_calls per cycle, unpaced,
		// earns 429s on all but the first few.
		// All three measured against mainnet.base.org, which rejects an
		// eleven-call batch outright and answers roughly five calls a second
		// however they are packaged.
		Attempts: 3, RetryPause: 750 * time.Millisecond, MinInterval: 150 * time.Millisecond, MaxBatch: 5,
		MinWindow: config.GetEnvDuration("HOLD_MIN_WINDOW", DefaultHoldMinWindow),
		MaxWindow: config.GetEnvDuration("HOLD_MAX_WINDOW", DefaultHoldMaxWindow),
		status:    map[string]Status{},
	}
	if s.pool == "" && len(s.comets) == 0 && len(rateFeeds[c.ID]) == 0 {
		return nil, fmt.Errorf("no verified Aave Pool, Comet or rate-feed addresses for chain %d", c.ID)
	}
	return s, nil
}

func (s *RPCSource) Name() string { return "rpc:" + s.Chain.Label }

// ErrNoCoverage means this chain has no verified address for that protocol, so
// there is nothing to read — as opposed to a read that failed.
var ErrNoCoverage = errors.New("no verified addresses for this protocol on this chain")

// protocols is the fixed set this source serves, so /sources lists a row even
// for a protocol that failed outright.
var rpcProtocols = []string{"aave-v3", "compound-v3", ProtocolHold}

// Fetch reads both protocols concurrently. One failing never fails the cycle —
// same rule as GraphSource, for the same reason.
func (s *RPCSource) Fetch(ctx context.Context) ([]venue.Venue, error) {
	// One Caller per cycle: it memoises, and a memoised rate is a wrong rate
	// five minutes from now.
	cl := NewCaller(s.rpc)
	cl.Attempts = s.Attempts
	cl.Pause = s.RetryPause
	cl.MinInterval = s.MinInterval
	cl.MaxBatch = s.MaxBatch

	readers := map[string]func(context.Context, *Caller, *Pricer) ([]venue.Venue, error){
		"aave-v3":     s.aave,
		"compound-v3": s.compound,
		ProtocolHold:  s.intrinsic,
	}

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		out []venue.Venue
		ok  bool
	)
	for _, protocol := range rpcProtocols {
		wg.Add(1)
		go func(protocol string) {
			defer wg.Done()
			st := Status{
				Protocol: protocol, Source: s.Name(),
				Chain: s.Chain.Label, ChainID: s.Chain.ID,
				LastAttempt: time.Now().UTC(),
			}
			p := NewPricer(ctx, s.Prices)
			venues, err := readers[protocol](ctx, cl, p)
			if err != nil {
				st.Error = err.Error()
				// A chain with no verified addresses for a protocol is a fact
				// about that chain, not an incident. It still shows in
				// /sources — silence there would read as "no venues here" —
				// but it does not page anyone every five minutes forever.
				if errors.Is(err, ErrNoCoverage) {
					slog.Info("rpc rate source has no coverage here", "protocol", protocol, "chain", s.Chain.Label, "err", err)
				} else {
					slog.Error("rpc rate read failed", "protocol", protocol, "chain", s.Chain.Label, "err", err)
				}
				s.setStatus(st, false)
				return
			}
			kept := venues[:0]
			for _, v := range venues {
				if s.Filter.Accept(v) {
					kept = append(kept, v)
				}
			}
			st.OK, st.Venues, st.LastSuccess = true, len(kept), st.LastAttempt
			st.Unpriceable = p.Unpriceable()
			s.setStatus(st, true)
			slog.Info("rpc rate read ok", "protocol", protocol, "chain", s.Chain.Label,
				"read", len(venues), "kept", len(kept))

			mu.Lock()
			out, ok = append(out, kept...), true
			mu.Unlock()
		}(protocol)
	}
	wg.Wait()

	if !ok {
		return nil, fmt.Errorf("every rpc rate read failed on %s", s.Chain.Label)
	}
	return out, nil
}

// Status reports the per-protocol outcome of the most recent fetch.
func (s *RPCSource) Status() []Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Status, 0, len(rpcProtocols))
	for _, protocol := range rpcProtocols {
		st, found := s.status[protocol]
		if !found {
			st = Status{Protocol: protocol, Source: s.Name(), Chain: s.Chain.Label, ChainID: s.Chain.ID}
		}
		out = append(out, st)
	}
	return out
}

func (s *RPCSource) setStatus(st Status, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok {
		if prev, found := s.status[st.Protocol]; found {
			st.LastSuccess, st.Venues = prev.LastSuccess, prev.Venues
		}
	}
	s.status[st.Protocol] = st
}

// --- Aave V3 ---

// Aave's ReserveData is a struct of static fields, so getReserveData returns a
// flat sequence of words. These are the ones we need; the count is asserted so
// a future layout change fails loudly instead of reading a borrow rate as a
// supply rate.
const (
	aaveReserveDataWords = 15
	wordConfiguration    = 0
	wordLiquidityRate    = 2
	wordAToken           = 8
)

// Configuration bitmap positions (aave-v3-core ReserveConfiguration.sol).
const (
	bitDecimals = 48 // 8 bits
	bitActive   = 56
	bitFrozen   = 57
	bitPaused   = 60
)

// aave reads every reserve the Pool lists. The Pool itself is the identity
// check: an address that does not answer getReservesList() is not a Pool.
func (s *RPCSource) aave(ctx context.Context, cl *Caller, p *Pricer) ([]venue.Venue, error) {
	if s.pool == "" {
		return nil, fmt.Errorf("%w: Aave V3 Pool on chain %d", ErrNoCoverage, s.Chain.ID)
	}
	list, err := cl.AddressList(ctx, s.pool, SelGetReservesList)
	if err != nil {
		return nil, fmt.Errorf("pool %s getReservesList(): %w", s.pool, err)
	}

	// Two batched round trips instead of two per reserve. A public node
	// rate-limits per request, so this is the difference between reading every
	// reserve and reading the first one.
	reads := make([]prices.Call, 0, len(list))
	for _, underlying := range list {
		reads = append(reads, prices.Call{To: s.pool, Data: AddressArg(SelGetReserveData, underlying)})
	}
	cl.Prefetch(ctx, reads)

	supplies := make([]prices.Call, 0, len(list))
	for _, underlying := range list {
		// Served from the memo the prefetch just filled; no second round trip.
		if aToken, err := s.aaveAToken(ctx, cl, underlying); err == nil {
			supplies = append(supplies, prices.Call{To: aToken, Data: SelTotalSupply})
		}
	}
	cl.Prefetch(ctx, supplies)

	now := nowUTC()
	out := make([]venue.Venue, 0, len(list))
	for _, underlying := range list {
		v, err := s.aaveReserve(ctx, cl, p, underlying, now)
		if err != nil {
			// Never a zero rate: say what could not be read and move on.
			slog.Warn("aave-v3 rpc: reserve omitted", "chain", s.Chain.Label,
				"reserve", underlying, "reason", err)
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// aaveAToken decodes just the aToken out of a reserve, so the totalSupply calls
// can be batched before anything else is decoded.
func (s *RPCSource) aaveAToken(ctx context.Context, cl *Caller, underlying string) (string, error) {
	ws, err := cl.Words(ctx, s.pool, AddressArg(SelGetReserveData, underlying), aaveReserveDataWords)
	if err != nil {
		return "", err
	}
	w := ws[wordAToken]
	if w.Sign() == 0 || w.BitLen() > 160 {
		return "", errors.New("reserve has no aToken")
	}
	h := w.Text(16)
	return "0x" + strings.Repeat("0", 40-len(h)) + h, nil
}

func (s *RPCSource) aaveReserve(ctx context.Context, cl *Caller, p *Pricer, underlying string, now time.Time) (venue.Venue, error) {
	ws, err := cl.Words(ctx, s.pool, AddressArg(SelGetReserveData, underlying), aaveReserveDataWords)
	if err != nil {
		return venue.Venue{}, err
	}
	cfg, rate := ws[wordConfiguration], ws[wordLiquidityRate]

	// Layout guard: currentLiquidityRate is a uint128, so a word too wide for
	// its type means the struct moved under us and we are reading some other
	// field as the supply rate.
	if rate.BitLen() > 128 {
		return venue.Venue{}, errors.New("unexpected getReserveData layout")
	}
	aToken, err := s.aaveAToken(ctx, cl, underlying)
	if err != nil {
		return venue.Venue{}, err
	}

	// A frozen, paused or inactive reserve is not a routable venue, whatever
	// its rate says.
	switch {
	case cfg.Bit(bitActive) == 0:
		return venue.Venue{}, errors.New("reserve is not active")
	case cfg.Bit(bitFrozen) == 1:
		return venue.Venue{}, errors.New("reserve is frozen")
	case cfg.Bit(bitPaused) == 1:
		return venue.Venue{}, errors.New("reserve is paused")
	}

	asset := ResolveAsset([]string{underlying}, "")
	if asset == "" {
		return venue.Venue{}, errors.New("underlying is not a canonical deposit asset")
	}
	decimals := bits(cfg, bitDecimals, 8)
	if decimals <= 0 || decimals > 36 {
		return venue.Venue{}, fmt.Errorf("implausible reserve decimals %d", decimals)
	}

	// The ray liquidity rate is an annual simple APR; the same per-second
	// compounding the subgraph path uses turns it into APY, so both sources
	// produce the same number for the same input.
	apy := RayRateToAPY(rate)

	supplyRaw, err := cl.Uint(ctx, aToken, SelTotalSupply)
	if err != nil {
		return venue.Venue{}, fmt.Errorf("aToken %s totalSupply(): %w", aToken, err)
	}
	price, ok := p.USD(asset)
	if !ok {
		return venue.Venue{}, errors.New("unpriceable: " + asset)
	}

	// The id must be byte-identical to the subgraph source's, or the executor
	// allowlist stops matching. Our deployments key an Aave reserve by the
	// underlying alone; see Reconcile for the upstream `underlying+provider`
	// form.
	poolID := strings.ToLower(underlying)
	return venue.Venue{
		ID:      venue.MakeID(s.Chain.Label, "aave-v3", poolID),
		Chain:   s.Chain.Label,
		Project: "aave-v3",
		// The canonical asset, not a symbol() call: the executor matches on it,
		// and it saves one eth_call per reserve against a rate-limited node.
		Symbol:     asset,
		PoolID:     poolID,
		Asset:      asset,
		TVLUsd:     decimalFloat(supplyRaw.String(), decimals) * price,
		APY:        apy,
		APYBase:    apy, // Aave pays no reward APR we can read on chain
		Stablecoin: isStable(asset),
		UpdatedAt:  now,
	}, nil
}

// bits reads an n-bit field starting at offset from a configuration bitmap.
func bits(cfg *big.Int, offset, n uint) int {
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), n), big.NewInt(1))
	return int(new(big.Int).And(new(big.Int).Rsh(cfg, offset), mask).Int64())
}

// --- Compound III ---

// cometMantissa is the 1e18 scale getSupplyRate answers in.
const cometMantissa = 1e18

func (s *RPCSource) compound(ctx context.Context, cl *Caller, p *Pricer) ([]venue.Venue, error) {
	if len(s.comets) == 0 {
		return nil, fmt.Errorf("%w: Comet markets on chain %d", ErrNoCoverage, s.Chain.ID)
	}
	// One batched round trip for the four constant reads plus utilization, then
	// one more for the rates those utilizations imply.
	reads := make([]prices.Call, 0, len(s.comets)*5)
	for _, m := range s.comets {
		for _, sel := range []string{SelSymbol, SelBaseToken, SelDecimals, SelTotalSupply, SelGetUtilization} {
			reads = append(reads, prices.Call{To: m.Address, Data: sel})
		}
	}
	cl.Prefetch(ctx, reads)

	rates := make([]prices.Call, 0, len(s.comets))
	for _, m := range s.comets {
		if util, err := cl.Uint(ctx, m.Address, SelGetUtilization); err == nil {
			rates = append(rates, prices.Call{To: m.Address, Data: UintArg(SelGetSupplyRate, util)})
		}
	}
	cl.Prefetch(ctx, rates)

	now := nowUTC()
	out := make([]venue.Venue, 0, len(s.comets))
	for _, m := range s.comets {
		v, err := s.comet(ctx, cl, p, m, now)
		if err != nil {
			slog.Warn("compound-v3 rpc: market omitted", "chain", s.Chain.Label,
				"comet", m.Address, "symbol", m.Symbol, "reason", err)
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *RPCSource) comet(ctx context.Context, cl *Caller, p *Pricer, m cometMarket, now time.Time) (venue.Venue, error) {
	// Verify before use: the table names a candidate, the contract confirms it.
	symbol, err := cl.Text(ctx, m.Address, SelSymbol)
	if err != nil {
		return venue.Venue{}, fmt.Errorf("symbol(): %w", err)
	}
	if !strings.EqualFold(symbol, m.Symbol) {
		return venue.Venue{}, fmt.Errorf("symbol() is %q, want %q", symbol, m.Symbol)
	}
	base, err := cl.Address(ctx, m.Address, SelBaseToken)
	if err != nil {
		return venue.Venue{}, fmt.Errorf("baseToken(): %w", err)
	}
	asset := ResolveAsset([]string{base}, "")
	if asset == "" {
		return venue.Venue{}, errors.New("base token " + base + " is not a canonical deposit asset")
	}
	decimals, err := cl.Uint8(ctx, m.Address, SelDecimals)
	if err != nil {
		return venue.Venue{}, fmt.Errorf("decimals(): %w", err)
	}

	// getSupplyRate returns a PER-SECOND rate scaled 1e18. It is not a
	// per-block rate: converting it as one would overstate every Compound
	// venue by the block time.
	util, err := cl.Uint(ctx, m.Address, SelGetUtilization)
	if err != nil {
		return venue.Venue{}, fmt.Errorf("getUtilization(): %w", err)
	}
	rate, err := cl.Uint(ctx, m.Address, UintArg(SelGetSupplyRate, util))
	if err != nil {
		return venue.Venue{}, fmt.Errorf("getSupplyRate(): %w", err)
	}
	apy := PerSecondMantissaToAPY(rate, cometMantissa)

	supplyRaw, err := cl.Uint(ctx, m.Address, SelTotalSupply)
	if err != nil {
		return venue.Venue{}, fmt.Errorf("totalSupply(): %w", err)
	}
	price, ok := p.USD(asset)
	if !ok {
		return venue.Venue{}, errors.New("unpriceable: " + asset)
	}

	poolID := strings.ToLower(m.Address)
	return venue.Venue{
		ID:      venue.MakeID(s.Chain.Label, "compound-v3", poolID),
		Chain:   s.Chain.Label,
		Project: "compound-v3",
		Symbol:  symbol,
		PoolID:  poolID,
		Asset:   asset,
		TVLUsd:  decimalFloat(supplyRaw.String(), decimals) * price,
		APY:     apy,
		APYBase: apy,
		// COMP emissions are NOT readable as an APR without a COMP/USD feed on
		// this chain, so they are left at zero here and taken from the subgraph
		// during reconciliation rather than guessed.
		APYReward:  0,
		Stablecoin: isStable(asset),
		UpdatedAt:  now,
	}, nil
}

// --- intrinsic yield (hold venues) ---

// A liquid staking token publishes no rate anywhere: the yield exists only as
// its exchange rate drifting upward. That is why these venues came from the
// subgraph, which derives the rate from a day of samples.
//
// The samples turn out not to need indexing. A Chainlink feed keeps its past
// rounds on chain, so getRoundData(latest - k) reads the same series directly
// and the rate is available on the first cycle instead of a day later.
const (
	// kindExchangeRate: an aggregator whose answer IS the exchange rate, so the
	// APY is the growth between two of its rounds. Only monotonic feeds belong
	// here — a MARKET price also drifts, and annualizing a market dip would
	// invent yield out of a discount.
	kindExchangeRate = "chainlink-exchange-rate"
	// kindSkySSR: Sky's oracle publishes the savings rate itself as a ray
	// per-second growth factor, so there is nothing to derive from history.
	kindSkySSR = "sky-ssr"
)

// rateFeed is one token whose holders earn by doing nothing, and the contract
// that says by how much.
type rateFeed struct {
	Token    string // the ERC-20 the user holds; also the venue's pool id
	Provider string // the rate contract
	Kind     string
	// Description is what description() must return, verbatim. It is the
	// identity check: these addresses are otherwise indistinguishable from any
	// other aggregator, and reading a market price as an exchange rate is the
	// exact mistake this table has to make impossible. Empty means the provider
	// has no description() (Sky's oracle reverts on it) and is identified by
	// its own accessor answering instead.
	Description string
}

// rateFeeds is the verified set per chain. Every entry was confirmed by
// calling description() on Base and by sampling historical rounds to prove the
// series only ever rises.
//
// Deliberately absent, and not to be added without new evidence:
//   - ezETH: the only ETH-denominated feed on Base is a market price, and it
//     FELL over 30 days. Annualizing that would invent yield.
//   - USDS: not yield-bearing. The savings rate accrues to sUSDS; giving USDS
//     an intrinsic APY would be the mirror image of the bug this code fixes.
//   - syrupUSDC: its rate lives on Ethereum, with no Base oracle.
//
// Base Sepolia has none of these contracts, so it has no intrinsic yield —
// which is the truth, not a gap.
var rateFeeds = map[int][]rateFeed{
	chains.BaseMainnet: {
		{"0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452", "0xB88BAc61a4Ca37C43a3725912B1f472c9A5bc061", kindExchangeRate, "wstETH-stETH Exchange Rate"},
		{"0x2ae3f1ec7f1f5012cfeab0185bfc7aa3cf0dec22", "0x868a501e68F3D1E89CfC0D22F6b22E8dabce5F04", kindExchangeRate, "cbETH-ETH Exchange Rate"},
		{"0x04c0599ae5a44757c0af6f9ec3b93da8976c150a", "0x35e9D7001819Ea3B39Da906aE6b06A62cfe2c181", kindExchangeRate, "weETH / eETH Exchange Rate"},
		{"0xedfa23602d0ec14714057867a78d01e94176bea0", "0xe8dD07CCf5BC4922424140E44Eb970F5950725ef", kindExchangeRate, "wrsETH-ETH Exchange Rate"},
		// Sky's SSR oracle: getSUSDSData() returns (ssr, chi, rho) — literally
		// sUSDS accounting, which is the identity check. ssr compounded per
		// second is 3.60%; chi drift measures 3.54%, and the gap is chi lagging
		// by rho rather than a disagreement.
		{"0x5875eee11cf8398102fdad704c9e96607675467a", "0x65d946e533748A998B1f0E430803e39A6388f7a1", kindSkySSR, ""},
	},
}

// roundOffsets are how far back to sample. These feeds publish on a 24h
// heartbeat with deviation updates in between, so consecutive rounds can be a
// minute apart and the wide offsets are what actually produce a usable window;
// AnnualizeGrowth then picks the widest pair it trusts.
var roundOffsets = []int64{1, 2, 5, 10}

// aggregatorRoundMask isolates the aggregator round from a proxy round id,
// which packs the phase in its high bits. Walking back past the start of a
// phase would cross into a different aggregator, so it is not done.
var aggregatorRoundMask = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))

func (s *RPCSource) intrinsic(ctx context.Context, cl *Caller, p *Pricer) ([]venue.Venue, error) {
	feeds := rateFeeds[s.Chain.ID]
	if len(feeds) == 0 {
		return nil, fmt.Errorf("%w: intrinsic rate feeds on chain %d", ErrNoCoverage, s.Chain.ID)
	}

	reads := make([]prices.Call, 0, len(feeds)*4)
	for _, f := range feeds {
		reads = append(reads,
			prices.Call{To: f.Token, Data: SelDecimals},
			prices.Call{To: f.Token, Data: SelTotalSupply})
		switch f.Kind {
		case kindExchangeRate:
			reads = append(reads,
				prices.Call{To: f.Provider, Data: SelDescription},
				prices.Call{To: f.Provider, Data: SelLatestRoundData})
		case kindSkySSR:
			reads = append(reads, prices.Call{To: f.Provider, Data: SelGetSUSDSData})
		}
	}
	cl.Prefetch(ctx, reads)

	// The historical rounds can only be named once the latest round id is
	// known, so they are a second batch rather than a call per sample.
	history := make([]prices.Call, 0, len(feeds)*len(roundOffsets))
	for _, f := range feeds {
		if f.Kind != kindExchangeRate {
			continue
		}
		ws, err := cl.Words(ctx, f.Provider, SelLatestRoundData, 5)
		if err != nil {
			continue
		}
		for _, id := range priorRoundIDs(ws[0]) {
			history = append(history, prices.Call{To: f.Provider, Data: UintArg(SelGetRoundData, id)})
		}
	}
	cl.Prefetch(ctx, history)

	now := nowUTC()
	out := make([]venue.Venue, 0, len(feeds))
	for _, f := range feeds {
		v, err := s.holdVenue(ctx, cl, p, f, now)
		if err != nil {
			// Unreadable, flat or falling: all of them mean "no rate", and none
			// of them means 0%. A zero here is the bug this adapter exists for.
			slog.Warn("hold rpc: venue omitted", "chain", s.Chain.Label,
				"token", f.Token, "provider", f.Provider, "reason", err)
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// priorRoundIDs turns a proxy round id into the earlier ids worth asking for,
// dropping any that would fall off the start of the current phase.
func priorRoundIDs(latest *big.Int) []*big.Int {
	if latest == nil || latest.Sign() <= 0 {
		return nil
	}
	round := new(big.Int).And(latest, aggregatorRoundMask)
	out := make([]*big.Int, 0, len(roundOffsets))
	for _, k := range roundOffsets {
		if round.Cmp(big.NewInt(k)) <= 0 {
			continue
		}
		out = append(out, new(big.Int).Sub(latest, big.NewInt(k)))
	}
	return out
}

func (s *RPCSource) holdVenue(ctx context.Context, cl *Caller, p *Pricer, f rateFeed, now time.Time) (venue.Venue, error) {
	var (
		apy float64
		err error
	)
	switch f.Kind {
	case kindExchangeRate:
		apy, err = s.exchangeRateAPY(ctx, cl, f)
	case kindSkySSR:
		apy, err = s.skySSRAPY(ctx, cl, f)
	default:
		err = errors.New("unknown rate feed kind " + f.Kind)
	}
	if err != nil {
		return venue.Venue{}, err
	}

	asset := ResolveAsset([]string{f.Token}, "")
	if asset == "" {
		return venue.Venue{}, errors.New("token is not a canonical deposit asset")
	}
	decimals, err := cl.Uint8(ctx, f.Token, SelDecimals)
	if err != nil {
		return venue.Venue{}, fmt.Errorf("decimals(): %w", err)
	}
	supplyRaw, err := cl.Uint(ctx, f.Token, SelTotalSupply)
	if err != nil {
		return venue.Venue{}, fmt.Errorf("totalSupply(): %w", err)
	}
	// TVL of a hold venue is the token's whole supply on this chain: every
	// holder is already in the position, there is nothing to deposit into.
	price, ok := p.USD(asset)
	if !ok {
		// Recorded by the Pricer, so /sources shows a measured rate we cannot
		// value rather than the venue simply vanishing.
		return venue.Venue{}, errors.New("unpriceable: " + asset)
	}

	poolID := strings.ToLower(f.Token)
	return venue.Venue{
		ID:      venue.MakeID(s.Chain.Label, ProtocolHold, poolID),
		Chain:   s.Chain.Label,
		Project: ProtocolHold,
		Symbol:  asset,
		PoolID:  poolID,
		Asset:   asset,
		TVLUsd:  decimalFloat(supplyRaw.String(), decimals) * price,
		APY:     apy,
		// No lending leg and no emissions: nobody borrows from your wallet.
		APYIntrinsic: apy,
		Stablecoin:   isStable(asset),
		UpdatedAt:    now,
	}, nil
}

// exchangeRateAPY annualizes the drift between two of the feed's own rounds.
// The feed's decimals never enter into it: only the ratio of two answers is
// used, and the scale cancels.
func (s *RPCSource) exchangeRateAPY(ctx context.Context, cl *Caller, f rateFeed) (float64, error) {
	// Identity first: an aggregator that describes itself as something else is
	// not this token's exchange rate, whatever the address looked like.
	if f.Description != "" {
		got, err := cl.Text(ctx, f.Provider, SelDescription)
		if err != nil {
			return 0, fmt.Errorf("description(): %w", err)
		}
		if strings.TrimSpace(got) != f.Description {
			return 0, fmt.Errorf("description() is %q, want %q", got, f.Description)
		}
	}

	latest, err := cl.Words(ctx, f.Provider, SelLatestRoundData, 5)
	if err != nil {
		return 0, fmt.Errorf("latestRoundData(): %w", err)
	}
	samples := make([]GrowthSample, 0, len(roundOffsets)+1)
	if sample, ok := roundSample(latest); ok {
		samples = append(samples, sample)
	}
	for _, id := range priorRoundIDs(latest[0]) {
		ws, err := cl.Words(ctx, f.Provider, UintArg(SelGetRoundData, id), 5)
		if err != nil {
			continue // one missing round is not a reason to discard the series
		}
		if sample, ok := roundSample(ws); ok {
			samples = append(samples, sample)
		}
	}

	apy, ok := AnnualizeGrowth("exchange rate "+f.Token, samples, s.MinWindow, s.MaxWindow)
	if !ok {
		// AnnualizeGrowth has already said why at debug level: too few samples,
		// too narrow a window, or a rate that did not rise.
		return 0, errors.New("no trustworthy growth in this feed's round history")
	}
	return apy, nil
}

// roundSample turns a round into a growth sample, rejecting the ones that are
// not evidence: an unanswered round, a negative answer, a zero timestamp.
func roundSample(ws []*big.Int) (GrowthSample, bool) {
	answer, updatedAt := ws[1], ws[3]
	if answer.Sign() <= 0 || answer.BitLen() >= 256 || updatedAt.Sign() <= 0 {
		return GrowthSample{}, false
	}
	return GrowthSample{At: time.Unix(updatedAt.Int64(), 0).UTC(), Value: answer}, true
}

// skySSRAPY reads the savings rate itself. getSUSDSData returns (ssr, chi, rho)
// and answering at all is the identity check: no other contract has it.
func (s *RPCSource) skySSRAPY(ctx context.Context, cl *Caller, f rateFeed) (float64, error) {
	ws, err := cl.Words(ctx, f.Provider, SelGetSUSDSData, 3)
	if err != nil {
		return 0, fmt.Errorf("getSUSDSData(): %w", err)
	}
	apy := RayPerSecondFactorToAPY(ws[0])
	if apy <= 0 {
		return 0, errors.New("savings rate is not above 1.0: no yield to report")
	}
	return apy, nil
}
