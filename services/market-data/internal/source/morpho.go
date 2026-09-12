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

const (
	DefaultMorphoMinWindow = 6 * time.Hour
	DefaultMorphoMaxWindow = 7 * 24 * time.Hour
)

type MorphoBlue struct {
	Chain     string
	ID        string
	MinWindow time.Duration
	MaxWindow time.Duration
}

const (
	morphoMaxVaults = 50
	morphoMaxSnaps  = 25
)

func NewMorphoBlue(chain, subgraphID string, minWindow, maxWindow time.Duration) *MorphoBlue {
	if minWindow <= 0 {
		minWindow = DefaultMorphoMinWindow
	}
	if maxWindow < minWindow {
		maxWindow = DefaultMorphoMaxWindow
	}
	return &MorphoBlue{Chain: chain, ID: subgraphID, MinWindow: minWindow, MaxWindow: maxWindow}
}

func (m *MorphoBlue) Protocol() string   { return "morpho-blue" }
func (m *MorphoBlue) SubgraphID() string { return m.ID }

func (m *MorphoBlue) Query() string {
	return fmt.Sprintf(`{
  vaults(first: %d, orderBy: totalAssets, orderDirection: desc, where: {totalSupply_gt: "0"}) {
    id
    name
    symbol
    asset
    assetSymbol
    assetDecimals
    totalAssets
    totalSupply
    sharePriceScaled
    fee
    curator
    lastUpdateTimestamp
    snapshots(first: %d, orderBy: hourIndex, orderDirection: desc) {
      timestamp
      sharePriceScaled
      totalAssets
    }
  }
}`, morphoMaxVaults, morphoMaxSnaps)
}

type morphoSnapshot struct {
	Timestamp        string `json:"timestamp"`
	SharePriceScaled string `json:"sharePriceScaled"`
	TotalAssets      string `json:"totalAssets"`
}

type morphoVaults struct {
	Vaults []struct {
		ID                  string           `json:"id"`
		Name                string           `json:"name"`
		Symbol              string           `json:"symbol"`
		Asset               string           `json:"asset"`
		AssetSymbol         string           `json:"assetSymbol"`
		AssetDecimals       int              `json:"assetDecimals"`
		TotalAssets         string           `json:"totalAssets"`
		TotalSupply         string           `json:"totalSupply"`
		SharePriceScaled    string           `json:"sharePriceScaled"`
		Fee                 string           `json:"fee"`
		Curator             string           `json:"curator"`
		LastUpdateTimestamp string           `json:"lastUpdateTimestamp"`
		Snapshots           []morphoSnapshot `json:"snapshots"`
	} `json:"vaults"`
}

func (m *MorphoBlue) Map(p *Pricer, raw json.RawMessage) ([]venue.Venue, error) {
	var res morphoVaults
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("morpho-blue: decode: %w", err)
	}

	now := nowUTC()
	out := make([]venue.Venue, 0, len(res.Vaults))
	for _, v := range res.Vaults {
		if supply := bigIntFromString(v.TotalSupply); supply == nil || supply.Sign() <= 0 {
			continue
		}
		apy, ok := m.vaultAPY(v.ID, v.Snapshots)
		if !ok {
			continue
		}
		asset := ResolveAsset([]string{v.Asset}, v.AssetSymbol)
		units := decimalFloat(v.TotalAssets, v.AssetDecimals)
		if units <= 0 {
			continue
		}
		price, ok := p.USD(asset)
		if !ok {
			slog.Warn("morpho-blue: vault dropped, unpriceable", "vault", v.ID, "asset", asset, "symbol", v.AssetSymbol)
			continue
		}
		tvl := units * price
		poolID := strings.ToLower(v.ID)
		out = append(out, venue.Venue{
			ID:         venue.MakeID(m.Chain, m.Protocol(), poolID),
			Chain:      m.Chain,
			Project:    m.Protocol(),
			Symbol:     v.Symbol,
			PoolID:     poolID,
			Asset:      asset,
			TVLUsd:     tvl,
			APY:        apy,
			APYBase:    apy,
			APYReward:  0,
			Stablecoin: isStable(asset),
			UpdatedAt:  now,
		})
	}
	return out, nil
}

func (m *MorphoBlue) vaultAPY(id string, snaps []morphoSnapshot) (float64, bool) {
	samples := make([]GrowthSample, 0, len(snaps))
	for _, s := range snaps {
		price := bigIntFromString(s.SharePriceScaled)
		ts := bigIntFromString(s.Timestamp)
		if price == nil || price.Sign() <= 0 || ts == nil || ts.Sign() <= 0 {
			continue
		}
		samples = append(samples, GrowthSample{At: time.Unix(ts.Int64(), 0).UTC(), Value: price})
	}
	return AnnualizeGrowth("morpho-blue vault "+id, samples, m.MinWindow, m.MaxWindow)
}

func init() {
	Register(func(c Chain) ProtocolAdapter {
		return NewMorphoBlue(c.Label,
			SubgraphID("MORPHO", c),
			config.GetEnvDuration("MORPHO_MIN_WINDOW", DefaultMorphoMinWindow),
			config.GetEnvDuration("MORPHO_MAX_WINDOW", DefaultMorphoMaxWindow),
		)
	})
}
