package source

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"strings"

	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

type AaveV3 struct {
	Chain string
	ID    string
}

const aaveMaxMarkets = 200

func NewAaveV3(chain, subgraphID string) *AaveV3 {
	return &AaveV3{Chain: chain, ID: subgraphID}
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
    variableBorrowIndex
    totalLiquidity
    availableLiquidity
    isActive
    isFrozen
    isPaused
    price { priceInEth oracle { baseCurrencyUnit } }
  }
}`, aaveMaxMarkets)
}

type aaveReserves struct {
	Reserves []struct {
		ID                  string `json:"id"`
		Symbol              string `json:"symbol"`
		Decimals            int    `json:"decimals"`
		UnderlyingAsset     string `json:"underlyingAsset"`
		LiquidityRate       string `json:"liquidityRate"`
		VariableBorrowIndex string `json:"variableBorrowIndex"`
		TotalLiquidity      string `json:"totalLiquidity"`
		AvailableLiquidity  string `json:"availableLiquidity"`
		IsActive            bool   `json:"isActive"`
		IsFrozen            bool   `json:"isFrozen"`
		IsPaused            bool   `json:"isPaused"`
		Price               struct {
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
		if !r.IsActive || r.IsFrozen || r.IsPaused || apy < 0 {
			continue
		}
		price := scaledFloat(r.Price.PriceInEth, r.Price.Oracle.BaseCurrencyUnit)
		if price <= 0 {
			var ok bool
			if price, ok = p.USD(asset); !ok {
				slog.Warn("aave-v3: reserve dropped, unpriceable", "reserve", r.ID, "asset", asset, "symbol", r.Symbol)
				continue
			}
		}
		poolID := strings.ToLower(r.ID)
		out = append(out, venue.Venue{
			LiquidityUsd:   max(0, decimalFloat(r.AvailableLiquidity, r.Decimals)) * price,
			LiquidityKnown: true,
			CollateralOnly: collateralOnly(bigIntFromString(r.VariableBorrowIndex), bigIntFromString(r.LiquidityRate)),
			ID:             venue.MakeID(a.Chain, a.Protocol(), poolID),
			Chain:          a.Chain,
			Project:        a.Protocol(),
			Symbol:         r.Symbol,
			PoolID:         poolID,
			Asset:          asset,
			TVLUsd:         supply * price,
			APY:            apy,
			APYBase:        apy,
			Stablecoin:     isStable(asset),
			UpdatedAt:      now,
		})
	}
	return out, nil
}

// A reserve that never accrued a borrow and pays nothing was opened as
// collateral only; its 0% is structural, not a failed measurement.
func collateralOnly(variableBorrowIndex, liquidityRate *big.Int) bool {
	return variableBorrowIndex != nil && liquidityRate != nil &&
		variableBorrowIndex.Cmp(rayOne) == 0 && liquidityRate.Sign() == 0
}

func scaledFloat(value, unit string) float64 {
	v, u := bigIntFromString(value), bigIntFromString(unit)
	if v == nil || u == nil || u.Sign() == 0 {
		return 0
	}
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(v), new(big.Float).SetInt(u)).Float64()
	return f
}

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
	Register(func(c Chain) ProtocolAdapter {
		return NewAaveV3(c.Label, SubgraphID("AAVE_V3", c))
	})
}
