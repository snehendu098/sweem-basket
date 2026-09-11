package source

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"

	"github.com/snehendu098/sweem-basket/internal/shared/config"

	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

// AaveV3BaseSubgraphID is the Aave V3 Base deployment on the decentralized network.
const AaveV3BaseSubgraphID = "GQFbb95cE6d8mV989mL5figjaGaKCQB3xqYrr1bRyXqF"

// AaveV3 maps Aave's `Reserve` entity onto our Venue model.
//
// Schema quirks this mapper absorbs (aave/protocol-subgraphs v3.schema.graphql):
//   - liquidityRate is a ray (1e27) *annual simple* rate -> RayRateToAPY
//   - totalLiquidity is raw token units -> scale by `decimals`
//   - price.priceInEth is misnamed on V3: it is the price in the oracle's base
//     currency (USD) scaled by oracle.baseCurrencyUnit (1e8)
//   - that oracle price is 0 for several live Base reserves (GHO, cbETH, wstETH,
//     EURC, weETH). Those reserves used to be dropped; they are now valued from
//     Chainlink instead. If Chainlink cannot price them either, they are still
//     dropped — never valued at a peg or at a similar asset's price.
type AaveV3 struct {
	Chain      string
	ID         string
	MaxMarkets int
}

func NewAaveV3(chain, subgraphID string) *AaveV3 {
	if subgraphID == "" {
		subgraphID = AaveV3BaseSubgraphID
	}
	return &AaveV3{Chain: chain, ID: subgraphID, MaxMarkets: 200}
}

func (a *AaveV3) Protocol() string   { return "aave-v3" }
func (a *AaveV3) SubgraphID() string { return a.ID }

func (a *AaveV3) Query() string {
	return fmt.Sprintf(`{
  reserves(first: %d, where: {isActive: true, isFrozen: false, isPaused: false}) {
    id
    symbol
    decimals
    underlyingAsset
    liquidityRate
    totalLiquidity
    isActive
    isFrozen
    isPaused
    price { priceInEth oracle { baseCurrencyUnit } }
  }
}`, a.MaxMarkets)
}

type aaveReserves struct {
	Reserves []struct {
		ID              string `json:"id"`
		Symbol          string `json:"symbol"`
		Decimals        int    `json:"decimals"`
		UnderlyingAsset string `json:"underlyingAsset"`
		LiquidityRate   string `json:"liquidityRate"`
		TotalLiquidity  string `json:"totalLiquidity"`
		IsActive        bool   `json:"isActive"`
		IsFrozen        bool   `json:"isFrozen"`
		IsPaused        bool   `json:"isPaused"`
		Price           struct {
			PriceInEth string `json:"priceInEth"`
			Oracle     struct {
				BaseCurrencyUnit string `json:"baseCurrencyUnit"`
			} `json:"oracle"`
		} `json:"price"`
	} `json:"reserves"`
}

func (a *AaveV3) Map(p *Pricer, raw json.RawMessage) ([]venue.Venue, error) {
	var res aaveReserves
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("aave-v3: decode: %w", err)
	}

	now := nowUTC()
	out := make([]venue.Venue, 0, len(res.Reserves))
	for _, r := range res.Reserves {
		apy := RayRateToAPY(bigIntFromString(r.LiquidityRate))
		supply := decimalFloat(r.TotalLiquidity, r.Decimals)
		asset := ResolveAsset([]string{r.UnderlyingAsset}, r.Symbol)
		// A frozen, paused or inactive reserve is not a routable venue, whatever
		// its rate says. The query filters these out; this is the belt-and-braces.
		if !r.IsActive || r.IsFrozen || r.IsPaused || apy <= 0 {
			continue
		}
		price := scaledFloat(r.Price.PriceInEth, r.Price.Oracle.BaseCurrencyUnit)
		if price <= 0 {
			// The venue's own oracle has no number for us; read the same market
			// from Chainlink rather than dropping a real reserve.
			var ok bool
			if price, ok = p.USD(asset); !ok {
				slog.Warn("aave-v3: reserve dropped, unpriceable", "reserve", r.ID, "asset", asset, "symbol", r.Symbol)
				continue
			}
		}
		out = append(out, venue.Venue{
			ID:         venue.MakeID(a.Chain, a.Protocol(), r.ID),
			Chain:      a.Chain,
			Project:    a.Protocol(),
			Symbol:     r.Symbol,
			PoolID:     r.ID,
			Asset:      asset,
			TVLUsd:     supply * price,
			APY:        apy,
			APYBase:    apy, // Aave reserves pay no reward APR in the subgraph
			Stablecoin: isStable(asset),
			UpdatedAt:  now,
		})
	}
	return out, nil
}

// scaledFloat divides a big integer string by a big integer unit string.
func scaledFloat(value, unit string) float64 {
	v, u := bigIntFromString(value), bigIntFromString(unit)
	if v == nil || u == nil || u.Sign() == 0 {
		return 0
	}
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(v), new(big.Float).SetInt(u)).Float64()
	return f
}

// decimalFloat divides a raw token amount by 10^decimals.
func decimalFloat(value string, decimals int) float64 {
	v := bigIntFromString(value)
	if v == nil || decimals < 0 {
		return 0
	}
	unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(v), new(big.Float).SetInt(unit)).Float64()
	return f
}

func bigIntFromString(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil
	}
	return v
}

func init() {
	Register(func(chain string) ProtocolAdapter {
		// No default: the constant above is a Base *mainnet* ID, and falling back
		// to it on another chain silently serves venues from the wrong network
		// rather than failing. Unset means unconfigured, same as morpho.
		return NewAaveV3(chain, config.GetEnv("AAVE_V3_SUBGRAPH_ID", ""))
	})
}
