package source

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/config"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

// ProtocolHold is the venue kind for an asset that earns by being held.
//
// Every other protocol here is a lending market: you supply, borrowers pay
// interest. A liquid staking token needs none of that — its exchange rate
// against the underlying rises whether it sits in a vault or in a wallet. The
// position IS holding the token, so a `hold` venue has no deposit call and its
// pool key is the token itself.
const ProtocolHold = "hold"

// Sampling bounds for exchange rates. Wider than the Morpho defaults on
// purpose: the Chainlink exchange-rate feeds these come from run a 24h
// heartbeat, so a six-hour window can legitimately contain zero rounds and
// would be read as "no yield". Overridable with HOLD_MIN_WINDOW /
// HOLD_MAX_WINDOW.
const (
	DefaultHoldMinWindow = 24 * time.Hour
	DefaultHoldMaxWindow = 14 * 24 * time.Hour
)

// Hold maps the sweem subgraph's `RateFeed` entities — one per yield-bearing
// token whose rate is readable on this chain — into venues.
//
// Rate convention: there is no rate field anywhere, only an exchange rate that
// drifts upward, so the APY is re-derived here from two hourly snapshots via
// AnnualizeGrowth, the same path Morpho share prices take. The subgraph also
// publishes its own `intrinsicApy`; it is deliberately not trusted, for the
// same reason the Morpho adapter re-derives instead of reading `supplyApy`.
//
// A feed the subgraph marked unavailable is DROPPED, never mapped to 0%. That
// distinction is the whole point of the adapter: "we could not measure this
// asset's yield" and "this asset yields nothing" route money to opposite places.
type Hold struct {
	Chain     string
	ID        string
	MaxFeeds  int
	MaxSnaps  int
	MinWindow time.Duration
	MaxWindow time.Duration
}

func NewHold(chain, subgraphID string, minWindow, maxWindow time.Duration) *Hold {
	if minWindow <= 0 {
		minWindow = DefaultHoldMinWindow
	}
	if maxWindow < minWindow {
		maxWindow = DefaultHoldMaxWindow
	}
	return &Hold{
		Chain: chain, ID: subgraphID,
		MaxFeeds: 50, MaxSnaps: 50,
		MinWindow: minWindow, MaxWindow: maxWindow,
	}
}

func (h *Hold) Protocol() string   { return ProtocolHold }
func (h *Hold) SubgraphID() string { return h.ID }

func (h *Hold) Query() string {
	return fmt.Sprintf(`{
  rateFeeds(first: %d) {
    id
    symbol
    decimals
    provider
    providerKind
    available
    unavailableReason
    totalSupply
    snapshots(first: %d, orderBy: hourIndex, orderDirection: desc) {
      timestamp
      rateScaled
    }
  }
}`, h.MaxFeeds, h.MaxSnaps)
}

type holdSnapshot struct {
	Timestamp  string `json:"timestamp"`
	RateScaled string `json:"rateScaled"`
}

type holdFeeds struct {
	RateFeeds []struct {
		ID                string         `json:"id"`
		Symbol            string         `json:"symbol"`
		Decimals          int            `json:"decimals"`
		Provider          string         `json:"provider"`
		ProviderKind      string         `json:"providerKind"`
		Available         bool           `json:"available"`
		UnavailableReason string         `json:"unavailableReason"`
		TotalSupply       string         `json:"totalSupply"`
		Snapshots         []holdSnapshot `json:"snapshots"`
	} `json:"rateFeeds"`
}

func (h *Hold) Map(p *Pricer, raw json.RawMessage) ([]venue.Venue, error) {
	var res holdFeeds
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("hold: decode: %w", err)
	}

	now := nowUTC()
	out := make([]venue.Venue, 0, len(res.RateFeeds))
	for _, f := range res.RateFeeds {
		if !f.Available {
			// The subgraph could not read or could not trust the rate. Say so and
			// move on; a zero here would be the bug this adapter exists to fix.
			slog.Warn("hold: intrinsic yield unavailable, no venue published",
				"token", f.ID, "symbol", f.Symbol, "provider", f.Provider,
				"provider_kind", f.ProviderKind, "reason", f.UnavailableReason)
			continue
		}
		apy, ok := h.feedAPY(f.ID, f.Symbol, f.Snapshots)
		if !ok {
			continue
		}
		asset := ResolveAsset([]string{f.ID}, f.Symbol)
		units := decimalFloat(f.TotalSupply, f.Decimals)
		if units <= 0 {
			continue
		}
		// TVL of a hold venue is the token's whole supply on this chain: every
		// holder is already in the position, there is nothing to deposit into.
		price, ok := p.USD(asset)
		if !ok {
			slog.Warn("hold: venue dropped, unpriceable", "token", f.ID, "asset", asset, "symbol", f.Symbol)
			continue
		}
		poolID := strings.ToLower(f.ID)
		out = append(out, venue.Venue{
			ID:      venue.MakeID(h.Chain, h.Protocol(), poolID),
			Chain:   h.Chain,
			Project: h.Protocol(),
			Symbol:  f.Symbol,
			PoolID:  poolID,
			Asset:   asset,
			TVLUsd:  units * price,
			APY:     apy,
			// No lending leg and no emissions: nobody borrows from your wallet.
			APYBase:      0,
			APYReward:    0,
			APYIntrinsic: apy,
			Stablecoin:   isStable(asset),
			UpdatedAt:    now,
		})
	}
	return out, nil
}

func (h *Hold) feedAPY(id, symbol string, snaps []holdSnapshot) (float64, bool) {
	samples := make([]GrowthSample, 0, len(snaps))
	for _, s := range snaps {
		rate := bigIntFromString(s.RateScaled)
		ts := bigIntFromString(s.Timestamp)
		if rate == nil || rate.Sign() <= 0 || ts == nil || ts.Sign() <= 0 {
			continue
		}
		samples = append(samples, GrowthSample{At: time.Unix(ts.Int64(), 0).UTC(), Value: rate})
	}
	return AnnualizeGrowth("intrinsic rate "+symbol+" "+id, samples, h.MinWindow, h.MaxWindow)
}

// StackIntrinsic folds each asset's intrinsic yield into every lending venue
// holding that asset, and is the reason the rest of this change is visible
// where it matters.
//
// Aave pays ~0.1% on wstETH because nobody borrows it, so it looks like a
// terrible venue — but a wstETH depositor keeps earning the ~3% staking rate
// underneath, and the two stack. Without this, /venues/best ranks a 1.2% USDC
// venue above a 3.1% wstETH one and the keeper calls that an improvement.
//
// APYBase is left alone: the split between borrower-paid yield and
// protocol-issuance yield is exactly what a consumer needs to weigh how durable
// a rate is.
func StackIntrinsic(venues []venue.Venue) []venue.Venue {
	// Keyed upper-case: the asset spelling that crosses the wire is the token's
	// own ("wstETH"), and matching it case-sensitively here is how a stray
	// upper-cased venue would silently lose its whole staking leg.
	rates := map[string]float64{}
	for _, v := range venues {
		if k := strings.ToUpper(v.Asset); v.Project == ProtocolHold && v.APYIntrinsic > rates[k] {
			rates[k] = v.APYIntrinsic
		}
	}
	if len(rates) == 0 {
		return venues
	}
	for i := range venues {
		if venues[i].Project == ProtocolHold {
			continue // already its own intrinsic rate; adding it twice doubles it
		}
		rate, ok := rates[strings.ToUpper(venues[i].Asset)]
		if !ok {
			continue
		}
		// Idempotent by construction: whatever intrinsic rate is already folded
		// in comes back out first. StackIntrinsic runs once inside GraphSource
		// and again inside Reconcile, and the second run may carry a fresher
		// rate read live off the chain — adding it on top of the first would
		// double-count the whole staking yield.
		venues[i].APY += rate - venues[i].APYIntrinsic
		venues[i].APYIntrinsic = rate
	}
	return venues
}

func init() {
	Register(func(c Chain) ProtocolAdapter {
		return NewHold(c.Label,
			SubgraphID("HOLD", c),
			config.GetEnvDuration("HOLD_MIN_WINDOW", DefaultHoldMinWindow),
			config.GetEnvDuration("HOLD_MAX_WINDOW", DefaultHoldMaxWindow),
		)
	})
}
