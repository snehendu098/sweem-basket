package source

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

type CompoundV3 struct {
	Chain string
	ID    string
}

const cometMaxMarkets = 50

func NewCompoundV3(chain, subgraphID string) *CompoundV3 {
	return &CompoundV3{Chain: chain, ID: subgraphID}
}

func (c *CompoundV3) Protocol() string   { return "compound-v3" }
func (c *CompoundV3) SubgraphID() string { return c.ID }

func (c *CompoundV3) Query() string {
	return fmt.Sprintf(`{
  markets(first: %d) {
    id
    configuration { symbol baseToken { token { address symbol decimals } } }
    accounting { totalBaseSupplyUsd supplyApr rewardSupplyApr netSupplyApr }
  }
}`, cometMaxMarkets)
}

type cometMarkets struct {
	Markets []struct {
		ID            string `json:"id"`
		Configuration struct {
			Symbol    string `json:"symbol"`
			BaseToken struct {
				Token struct {
					Address string `json:"address"`
					Symbol  string `json:"symbol"`
				} `json:"token"`
			} `json:"baseToken"`
		} `json:"configuration"`
		Accounting struct {
			TotalBaseSupplyUsd string `json:"totalBaseSupplyUsd"`
			SupplyApr          string `json:"supplyApr"`
			RewardSupplyApr    string `json:"rewardSupplyApr"`
			NetSupplyApr       string `json:"netSupplyApr"`
		} `json:"accounting"`
	} `json:"markets"`
}

func (c *CompoundV3) Map(_ *Pricer, raw json.RawMessage) ([]venue.Venue, error) {
	var res cometMarkets
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("compound-v3: decode: %w", err)
	}

	now := nowUTC()
	out := make([]venue.Venue, 0, len(res.Markets))
	for _, mk := range res.Markets {
		base := parseDecimal(mk.Accounting.SupplyApr)
		net := parseDecimal(mk.Accounting.NetSupplyApr)
		if net <= 0 {
			net = base
		}
		apy := APRToAPY(net)
		apyBase := APRToAPY(base)
		tvl := parseDecimal(mk.Accounting.TotalBaseSupplyUsd)
		if apy < 0 || tvl <= 0 {
			continue
		}
		tok := mk.Configuration.BaseToken.Token
		asset := ResolveAsset([]string{tok.Address}, tok.Symbol)
		poolID := strings.ToLower(mk.ID)
		out = append(out, venue.Venue{
			ID:         venue.MakeID(c.Chain, c.Protocol(), poolID),
			Chain:      c.Chain,
			Project:    c.Protocol(),
			Symbol:     mk.Configuration.Symbol,
			PoolID:     poolID,
			Asset:      asset,
			TVLUsd:     tvl,
			APY:        apy,
			APYBase:    apyBase,
			APYReward:  apy - apyBase,
			Stablecoin: isStable(asset),
			UpdatedAt:  now,
		})
	}
	return out, nil
}

func parseDecimal(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func init() {
	Register(func(c Chain) ProtocolAdapter {
		return NewCompoundV3(c.Label, SubgraphID("COMPOUND_V3", c))
	})
}
