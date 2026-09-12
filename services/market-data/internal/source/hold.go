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

const ProtocolHold = "hold"

const (
	DefaultHoldMinWindow = 24 * time.Hour
	DefaultHoldMaxWindow = 14 * 24 * time.Hour
)

type Hold struct {
	Chain     string
	ID        string
	MinWindow time.Duration
	MaxWindow time.Duration
}

const (
	holdMaxFeeds = 50
	holdMaxSnaps = 50
)

func NewHold(chain, subgraphID string, minWindow, maxWindow time.Duration) *Hold {
	if minWindow <= 0 {
		minWindow = DefaultHoldMinWindow
	}
	if maxWindow < minWindow {
		maxWindow = DefaultHoldMaxWindow
	}
	return &Hold{Chain: chain, ID: subgraphID, MinWindow: minWindow, MaxWindow: maxWindow}
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
}`, holdMaxFeeds, holdMaxSnaps)
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
		price, ok := p.USD(asset)
		if !ok {
			slog.Warn("hold: venue dropped, unpriceable", "token", f.ID, "asset", asset, "symbol", f.Symbol)
			continue
		}
		poolID := strings.ToLower(f.ID)
		out = append(out, venue.Venue{
			LiquidityUsd:   units * price,
			LiquidityKnown: true,
			ID:             venue.MakeID(h.Chain, h.Protocol(), poolID),
			Chain:          h.Chain,
			Project:        h.Protocol(),
			Symbol:         f.Symbol,
			PoolID:         poolID,
			Asset:          asset,
			TVLUsd:         units * price,
			APY:            apy,
			APYBase:        0,
			APYReward:      0,
			APYIntrinsic:   apy,
			Stablecoin:     isStable(asset),
			UpdatedAt:      now,
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

func StackIntrinsic(venues []venue.Venue) []venue.Venue {
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
			continue
		}
		rate, ok := rates[strings.ToUpper(venues[i].Asset)]
		if !ok {
			continue
		}
		// Replace, never add: this runs in GraphSource and again in Reconcile,
		// and adding would double-count the staking yield.
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
