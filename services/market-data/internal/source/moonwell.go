package source

import (
	"encoding/json"
	"fmt"

	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

type Moonwell struct {
	Chain string
	ID    string
}

const moonwellMaxMarkets = 100

func NewMoonwell(chain, subgraphID string) *Moonwell {
	return &Moonwell{Chain: chain, ID: subgraphID}
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
    totalBorrowBalanceUSD
    inputToken { id symbol decimals }
    rates { side type rate }
  }
}`, moonwellMaxMarkets)
}

type moonwellMarkets struct {
	Markets []struct {
		ID                     string `json:"id"`
		Name                   string `json:"name"`
		IsActive               bool   `json:"isActive"`
		TotalDepositBalanceUSD string `json:"totalDepositBalanceUSD"`
		TotalBorrowBalanceUSD  string `json:"totalBorrowBalanceUSD"`
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
				apy = parseDecimal(r.Rate)
				break
			}
		}
		tvl := parseDecimal(mk.TotalDepositBalanceUSD)
		if !mk.IsActive || apy < 0 || tvl <= 0 {
			continue
		}
		asset := ResolveAsset([]string{mk.InputToken.ID}, mk.InputToken.Symbol)
		out = append(out, venue.Venue{
			// Deposits minus borrows is a lower bound on getCash(): the cToken
			// also holds its reserves, so this never overstates the exit.
			LiquidityUsd:   max(0, tvl-parseDecimal(mk.TotalBorrowBalanceUSD)),
			LiquidityKnown: true,
			ID:             venue.MakeID(m.Chain, m.Protocol(), mk.ID),
			Chain:          m.Chain,
			Project:        m.Protocol(),
			Symbol:         mk.InputToken.Symbol,
			PoolID:         mk.ID,
			Asset:          asset,
			TVLUsd:         tvl,
			APY:            apy,
			APYBase:        apy,
			Stablecoin:     isStable(asset),
			UpdatedAt:      now,
		})
	}
	return out, nil
}

func init() {
	Register(func(c Chain) ProtocolAdapter {
		return NewMoonwell(c.Label, SubgraphID("MOONWELL", c))
	})
}
