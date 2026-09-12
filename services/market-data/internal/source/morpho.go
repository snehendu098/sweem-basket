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

// Default bounds on the share-price sampling window. Overridable with
// MORPHO_MIN_WINDOW / MORPHO_MAX_WINDOW (Go duration strings).
const (
	DefaultMorphoMinWindow = 6 * time.Hour
	DefaultMorphoMaxWindow = 7 * 24 * time.Hour
)

// MorphoBlue maps MetaMorpho `Vault` entities (subgraph/morpho-blue-base) onto
// our Venue model.
//
// Rate convention: Morpho publishes NO rate field. Yield exists only as
// appreciation of totalAssets/totalSupply, so the APY is derived from two
// hourly snapshots via SharePriceGrowthToAPY. sharePriceScaled is
// totalAssets*1e36/totalSupply as a BigInt; the 1e36 cancels in the ratio, and
// the ratio is taken in math/big because a 1e36-scaled integer does not survive
// a float64 round trip.
//
// Other quirks absorbed here:
//   - snapshots are event-driven hourly buckets, so they are IRREGULARLY spaced:
//     elapsed comes from the two `timestamp`s we actually got, never assumed.
//   - the subgraph carries no USD price at all, so TVL is valued with the shared
//     Chainlink Pricer. Vaults whose asset has no usable feed are dropped, not
//     assumed to be worth a dollar.
//   - APYReward is structurally 0: MORPHO emissions are paid by an off-vault
//     Universal Rewards Distributor and are not on-chain in this subgraph.
//     Fabricating a number here would make the router chase yield that the
//     vault does not actually pay.
type MorphoBlue struct {
	Chain     string
	ID        string
	MaxVaults int
	MaxSnaps  int
	MinWindow time.Duration
	MaxWindow time.Duration
}

// NewMorphoBlue builds the adapter. There is no default subgraph id: the
// deployment is ours and per chain, so an unset MORPHO_SUBGRAPH_ID_<chain>
// leaves the adapter present-but-unconfigured rather than pointing at nothing.
func NewMorphoBlue(chain, subgraphID string, minWindow, maxWindow time.Duration) *MorphoBlue {
	if minWindow <= 0 {
		minWindow = DefaultMorphoMinWindow
	}
	if maxWindow < minWindow {
		maxWindow = DefaultMorphoMaxWindow
	}
	return &MorphoBlue{
		Chain: chain, ID: subgraphID,
		MaxVaults: 50, MaxSnaps: 25,
		MinWindow: minWindow, MaxWindow: maxWindow,
	}
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
}`, m.MaxVaults, m.MaxSnaps)
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
		// Guard the division that produced sharePriceScaled upstream: an empty
		// vault has no price and no rate.
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
		// The subgraph reports token units only. No price, no venue: a wrong TVL
		// misroutes just as badly as a wrong APY.
		price, ok := p.USD(asset)
		if !ok {
			slog.Warn("morpho-blue: vault dropped, unpriceable", "vault", v.ID, "asset", asset, "symbol", v.AssetSymbol)
			continue
		}
		tvl := units * price
		poolID := strings.ToLower(v.ID) // executor allowlist keys on lowercase hex
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
			APYReward:  0, // see type comment: off-vault URD, not in this subgraph
			Stablecoin: isStable(asset),
			UpdatedAt:  now,
		})
	}
	return out, nil
}

// vaultAPY derives the APY from the widest usable snapshot pair, or reports
// !ok so the caller drops the vault. The rules for which samples are
// trustworthy are shared with the intrinsic-rate sampler in growth.go:
// publishing a rate we do not trust is worse than publishing no venue at all.
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
