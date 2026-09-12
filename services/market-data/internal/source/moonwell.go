package source

import (
	"encoding/json"
	"fmt"

	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

// Moonwell maps the Messari lending `Market` entity onto our Venue model.
//
// Rate convention: Messari's InterestRate.rate is ALREADY an APY percentage
// (LENDER/VARIABLE side), so this mapper does no rate math at all — the whole
// point of keeping conversions in apy.go is that only the protocols that need
// them pay for them.
//
// Other quirks absorbed here:
//   - yield lives in a nested list, not a field: pick side == "LENDER"
//   - TVL is pre-computed in USD (totalDepositBalanceUSD), no price join needed
type Moonwell struct {
	Chain      string
	ID         string
	MaxMarkets int
}

// NewMoonwell takes the subgraph id for one chain; see NewAaveV3 on why an
// unset id is never defaulted.
func NewMoonwell(chain, subgraphID string) *Moonwell {
	return &Moonwell{Chain: chain, ID: subgraphID, MaxMarkets: 100}
}

func (m *Moonwell) Protocol() string   { return "moonwell" }
func (m *Moonwell) SubgraphID() string { return m.ID }

func (m *Moonwell) Query() string {
	return fmt.Sprintf(`{
  markets(first: %d, orderBy: totalDepositBalanceUSD, orderDirection: desc, where: {isActive: true}) {
    id
    name
    isActive
    totalDepositBalanceUSD
    inputToken { id symbol decimals }
    rates { side type rate }
  }
}`, m.MaxMarkets)
}

type moonwellMarkets struct {
	Markets []struct {
		ID                     string `json:"id"`
		Name                   string `json:"name"`
		IsActive               bool   `json:"isActive"`
		TotalDepositBalanceUSD string `json:"totalDepositBalanceUSD"`
		InputToken             struct {
			ID     string `json:"id"`
			Symbol string `json:"symbol"`
		} `json:"inputToken"`
		Rates []struct {
			Side string `json:"side"`
			Type string `json:"type"`
			Rate string `json:"rate"`
		} `json:"rates"`
	} `json:"markets"`
}

func (m *Moonwell) Map(_ *Pricer, raw json.RawMessage) ([]venue.Venue, error) {
	var res moonwellMarkets
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("moonwell: decode: %w", err)
	}

	now := nowUTC()
	out := make([]venue.Venue, 0, len(res.Markets))
	for _, mk := range res.Markets {
		var apy float64
		for _, r := range mk.Rates {
			if r.Side == "LENDER" {
				apy = parseDecimal(r.Rate) // already an APY percentage
				break
			}
		}
		tvl := parseDecimal(mk.TotalDepositBalanceUSD)
		// Zero APY is left to Filter, per chain.
		if !mk.IsActive || apy < 0 || tvl <= 0 {
			continue
		}
		asset := ResolveAsset([]string{mk.InputToken.ID}, mk.InputToken.Symbol)
		out = append(out, venue.Venue{
			ID:      venue.MakeID(m.Chain, m.Protocol(), mk.ID),
			Chain:   m.Chain,
			Project: m.Protocol(),
			Symbol:  mk.InputToken.Symbol,
			PoolID:  mk.ID,
			Asset:   asset,
			TVLUsd:  tvl,
			APY:     apy,
			// Messari's LENDER rate folds WELL emissions into one number; the
			// subgraph does not split base from reward.
			APYBase:    apy,
			Stablecoin: isStable(asset),
			UpdatedAt:  now,
		})
	}
	return out, nil
}

func init() {
	Register(func(c Chain) ProtocolAdapter {
		return NewMoonwell(c.Label, SubgraphID("MOONWELL", c))
	})
}
