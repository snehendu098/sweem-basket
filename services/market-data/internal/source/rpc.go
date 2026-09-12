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

type RPCSource struct {
	Chain  Chain
	Filter Filter
	Prices PriceFeed

	rpc         prices.RPC
	pool        string
	comets      []cometMarket
	Attempts    int
	RetryPause  time.Duration
	MinInterval time.Duration
	MaxBatch    int
	MinWindow   time.Duration
	MaxWindow   time.Duration

	mu     sync.RWMutex
	status map[string]Status
}

var aavePools = map[int]string{
	chains.BaseMainnet: "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5",
	chains.BaseSepolia: "0x07eA79F68B2B3df564D0A34F8e19D9B1e339814b",
}

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

var ErrNoCoverage = errors.New("no verified addresses for this protocol on this chain")

var rpcProtocols = []string{"aave-v3", "compound-v3", ProtocolHold}

func (s *RPCSource) Fetch(ctx context.Context) ([]venue.Venue, error) {
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
				if errors.Is(err, ErrNoCoverage) {
					slog.Info("rpc rate source has no coverage here", "protocol", protocol, "chain", s.Chain.Label, "err", err)
				} else {
					slog.Error("rpc rate read failed", "protocol", protocol, "chain", s.Chain.Label, "err", err)
				}
				s.setStatus(st, false)
				return
			}
			kept := venues[:0]
			for i := range venues {
				if s.Filter.Screen(&venues[i]) {
					kept = append(kept, venues[i])
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

const (
	aaveReserveDataWords    = 15
	wordConfiguration       = 0
	wordLiquidityRate       = 2
	wordVariableBorrowIndex = 3
	wordAToken              = 8
)

const (
	bitDecimals = 48
	bitActive   = 56
	bitFrozen   = 57
	bitPaused   = 60
)

func (s *RPCSource) aave(ctx context.Context, cl *Caller, p *Pricer) ([]venue.Venue, error) {
	if s.pool == "" {
		return nil, fmt.Errorf("%w: Aave V3 Pool on chain %d", ErrNoCoverage, s.Chain.ID)
	}
	list, err := cl.AddressList(ctx, s.pool, SelGetReservesList)
	if err != nil {
		return nil, fmt.Errorf("pool %s getReservesList(): %w", s.pool, err)
	}

	reads := make([]prices.Call, 0, len(list))
	for _, underlying := range list {
		reads = append(reads, prices.Call{To: s.pool, Data: AddressArg(SelGetReserveData, underlying)})
	}
	cl.Prefetch(ctx, reads)

	supplies := make([]prices.Call, 0, len(list))
	for _, underlying := range list {
		if aToken, err := s.aaveAToken(ctx, cl, underlying); err == nil {
			supplies = append(supplies,
				prices.Call{To: aToken, Data: SelTotalSupply},
				prices.Call{To: underlying, Data: AddressArg(SelBalanceOf, aToken)})
		}
	}
	cl.Prefetch(ctx, supplies)

	now := nowUTC()
	out := make([]venue.Venue, 0, len(list))
	for _, underlying := range list {
		v, err := s.aaveReserve(ctx, cl, p, underlying, now)
		if err != nil {
			slog.Warn("aave-v3 rpc: reserve omitted", "chain", s.Chain.Label,
				"reserve", underlying, "reason", err)
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

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
	cfg, rate, borrowIndex := ws[wordConfiguration], ws[wordLiquidityRate], ws[wordVariableBorrowIndex]

	if rate.BitLen() > 128 {
		return venue.Venue{}, errors.New("unexpected getReserveData layout")
	}
	aToken, err := s.aaveAToken(ctx, cl, underlying)
	if err != nil {
		return venue.Venue{}, err
	}

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

	apy := RayRateToAPY(rate)

	supplyRaw, err := cl.Uint(ctx, aToken, SelTotalSupply)
	if err != nil {
		return venue.Venue{}, fmt.Errorf("aToken %s totalSupply(): %w", aToken, err)
	}
	cashRaw, err := cl.Uint(ctx, underlying, AddressArg(SelBalanceOf, aToken))
	if err != nil {
		return venue.Venue{}, fmt.Errorf("%s balanceOf(aToken): %w", underlying, err)
	}
	price, ok := p.USD(asset)
	if !ok {
		return venue.Venue{}, errors.New("unpriceable: " + asset)
	}

	poolID := strings.ToLower(underlying)
	return venue.Venue{
		LiquidityUsd:   decimalFloat(cashRaw.String(), decimals) * price,
		LiquidityKnown: true,
		CollateralOnly: collateralOnly(borrowIndex, rate),
		ID:             venue.MakeID(s.Chain.Label, "aave-v3", poolID),
		Chain:          s.Chain.Label,
		Project:        "aave-v3",
		Symbol:         asset,
		PoolID:         poolID,
		Asset:          asset,
		TVLUsd:         decimalFloat(supplyRaw.String(), decimals) * price,
		APY:            apy,
		APYBase:        apy,
		Stablecoin:     isStable(asset),
		UpdatedAt:      now,
	}, nil
}

func bits(cfg *big.Int, offset, n uint) int {
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), n), big.NewInt(1))
	return int(new(big.Int).And(new(big.Int).Rsh(cfg, offset), mask).Int64())
}

const cometMantissa = 1e18

func (s *RPCSource) compound(ctx context.Context, cl *Caller, p *Pricer) ([]venue.Venue, error) {
	if len(s.comets) == 0 {
		return nil, fmt.Errorf("%w: Comet markets on chain %d", ErrNoCoverage, s.Chain.ID)
	}
	reads := make([]prices.Call, 0, len(s.comets)*5)
	for _, m := range s.comets {
		for _, sel := range []string{SelSymbol, SelBaseToken, SelDecimals, SelTotalSupply, SelGetUtilization} {
			reads = append(reads, prices.Call{To: m.Address, Data: sel})
		}
	}
	cl.Prefetch(ctx, reads)

	rates := make([]prices.Call, 0, len(s.comets)*2)
	for _, m := range s.comets {
		if util, err := cl.Uint(ctx, m.Address, SelGetUtilization); err == nil {
			rates = append(rates, prices.Call{To: m.Address, Data: UintArg(SelGetSupplyRate, util)})
		}
		if base, err := cl.Address(ctx, m.Address, SelBaseToken); err == nil {
			rates = append(rates, prices.Call{To: base, Data: AddressArg(SelBalanceOf, m.Address)})
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

	util, err := cl.Uint(ctx, m.Address, SelGetUtilization)
	if err != nil {
		return venue.Venue{}, fmt.Errorf("getUtilization(): %w", err)
	}
	rate, err := cl.Uint(ctx, m.Address, UintArg(SelGetSupplyRate, util))
	if err != nil {
		return venue.Venue{}, fmt.Errorf("getSupplyRate(): %w", err)
	}
	// Comet's getSupplyRate is PER SECOND, unlike Compound V2's per block.
	apy := PerSecondMantissaToAPY(rate, cometMantissa)

	supplyRaw, err := cl.Uint(ctx, m.Address, SelTotalSupply)
	if err != nil {
		return venue.Venue{}, fmt.Errorf("totalSupply(): %w", err)
	}
	cashRaw, err := cl.Uint(ctx, base, AddressArg(SelBalanceOf, m.Address))
	if err != nil {
		return venue.Venue{}, fmt.Errorf("baseToken balanceOf(comet): %w", err)
	}
	price, ok := p.USD(asset)
	if !ok {
		return venue.Venue{}, errors.New("unpriceable: " + asset)
	}

	poolID := strings.ToLower(m.Address)
	return venue.Venue{
		LiquidityUsd:   decimalFloat(cashRaw.String(), decimals) * price,
		LiquidityKnown: true,
		ID:             venue.MakeID(s.Chain.Label, "compound-v3", poolID),
		Chain:          s.Chain.Label,
		Project:        "compound-v3",
		Symbol:         symbol,
		PoolID:         poolID,
		Asset:          asset,
		TVLUsd:         decimalFloat(supplyRaw.String(), decimals) * price,
		APY:            apy,
		APYBase:        apy,
		APYReward:      0,
		Stablecoin:     isStable(asset),
		UpdatedAt:      now,
	}, nil
}

const (
	kindExchangeRate = "chainlink-exchange-rate"
	kindSkySSR       = "sky-ssr"
)

type rateFeed struct {
	Token       string
	Provider    string
	Kind        string
	Description string
}

var rateFeeds = map[int][]rateFeed{
	chains.BaseMainnet: {
		{"0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452", "0xB88BAc61a4Ca37C43a3725912B1f472c9A5bc061", kindExchangeRate, "wstETH-stETH Exchange Rate"},
		{"0x2ae3f1ec7f1f5012cfeab0185bfc7aa3cf0dec22", "0x868a501e68F3D1E89CfC0D22F6b22E8dabce5F04", kindExchangeRate, "cbETH-ETH Exchange Rate"},
		{"0x04c0599ae5a44757c0af6f9ec3b93da8976c150a", "0x35e9D7001819Ea3B39Da906aE6b06A62cfe2c181", kindExchangeRate, "weETH / eETH Exchange Rate"},
		{"0xedfa23602d0ec14714057867a78d01e94176bea0", "0xe8dD07CCf5BC4922424140E44Eb970F5950725ef", kindExchangeRate, "wrsETH-ETH Exchange Rate"},
		{"0x5875eee11cf8398102fdad704c9e96607675467a", "0x65d946e533748A998B1f0E430803e39A6388f7a1", kindSkySSR, ""},
	},
}

var roundOffsets = []int64{1, 2, 5, 10}

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
			slog.Warn("hold rpc: venue omitted", "chain", s.Chain.Label,
				"token", f.Token, "provider", f.Provider, "reason", err)
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

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
	price, ok := p.USD(asset)
	if !ok {
		return venue.Venue{}, errors.New("unpriceable: " + asset)
	}

	poolID := strings.ToLower(f.Token)
	tvl := decimalFloat(supplyRaw.String(), decimals) * price
	return venue.Venue{
		LiquidityUsd:   tvl,
		LiquidityKnown: true,
		ID:             venue.MakeID(s.Chain.Label, ProtocolHold, poolID),
		Chain:          s.Chain.Label,
		Project:        ProtocolHold,
		Symbol:         asset,
		PoolID:         poolID,
		Asset:          asset,
		TVLUsd:         decimalFloat(supplyRaw.String(), decimals) * price,
		APY:            apy,
		APYIntrinsic:   apy,
		Stablecoin:     isStable(asset),
		UpdatedAt:      now,
	}, nil
}

func (s *RPCSource) exchangeRateAPY(ctx context.Context, cl *Caller, f rateFeed) (float64, error) {
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
			continue
		}
		if sample, ok := roundSample(ws); ok {
			samples = append(samples, sample)
		}
	}

	apy, ok := AnnualizeGrowth("exchange rate "+f.Token, samples, s.MinWindow, s.MaxWindow)
	if !ok {
		return 0, errors.New("no trustworthy growth in this feed's round history")
	}
	return apy, nil
}

func roundSample(ws []*big.Int) (GrowthSample, bool) {
	answer, updatedAt := ws[1], ws[3]
	if answer.Sign() <= 0 || answer.BitLen() >= 256 || updatedAt.Sign() <= 0 {
		return GrowthSample{}, false
	}
	return GrowthSample{At: time.Unix(updatedAt.Int64(), 0).UTC(), Value: answer}, true
}

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
