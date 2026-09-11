package source

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/snehendu098/sweem-basket/internal/shared/config"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

// CompoundV3BaseSubgraphID is the live Compound III (Comet) Base deployment.
const CompoundV3BaseSubgraphID = "2hcXhs36pTBDVUmk5K2Zkr6N4UYGwaHuco2a6jyTsijo"

// CompoundV3 maps Comet's `Market` + `MarketAccounting` onto our Venue model.
//
// Rate convention: supplyApr / netSupplyApr are decimal FRACTIONS of an annual
// simple rate (0.0201 == 2.01% APR), unlike Aave's ray and unlike Moonwell's
// ready-made percentage. Compound III accrues every second, so the same
// APRToAPY compounding used for Aave applies -- only the input scale differs.
//
// Other quirks absorbed here:
//   - only the market's base token earns supply yield; collateral does not
//   - netSupplyApr = supplyApr + rewardSupplyApr (COMP emissions)
//   - TVL is already in USD on the accounting entity
type CompoundV3 struct {
	Chain      string
	ID         string
	MaxMarkets int
}

func NewCompoundV3(chain, subgraphID string) *CompoundV3 {
	// Deliberately no fallback to CompoundV3BaseSubgraphID: that is a Base *mainnet*
	// deployment, and substituting it when unconfigured serves venues from the
	// wrong network instead of failing. An empty id reports as unconfigured.
	return &CompoundV3{Chain: chain, ID: subgraphID, MaxMarkets: 50}
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
}`, c.MaxMarkets)
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
		if apy <= 0 || tvl <= 0 {
			continue
		}
		tok := mk.Configuration.BaseToken.Token
		asset := ResolveAsset([]string{tok.Address}, tok.Symbol)
		out = append(out, venue.Venue{
			ID:         venue.MakeID(c.Chain, c.Protocol(), mk.ID),
			Chain:      c.Chain,
			Project:    c.Protocol(),
			Symbol:     mk.Configuration.Symbol,
			PoolID:     mk.ID,
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

// parseDecimal reads a GraphQL BigDecimal, which arrives as a JSON string.
func parseDecimal(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func init() {
	Register(func(chain string) ProtocolAdapter {
		// No default: the constant above is a Base *mainnet* ID, and falling back
		// to it on another chain silently serves venues from the wrong network
		// rather than failing. Unset means unconfigured, same as morpho.
		return NewCompoundV3(chain, config.GetEnv("COMPOUND_V3_SUBGRAPH_ID", ""))
	})
}
