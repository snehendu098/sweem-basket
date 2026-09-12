package source

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

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

// NewCompoundV3 takes the subgraph id for one chain; see NewAaveV3 on why an
// unset id is never defaulted.
func NewCompoundV3(chain, subgraphID string) *CompoundV3 {
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
		// Zero APY is left to Filter: on a testnet a market pays 0% because
		// nobody borrows, which is idle, not broken.
		if apy < 0 || tvl <= 0 {
			continue
		}
		tok := mk.Configuration.BaseToken.Token
		asset := ResolveAsset([]string{tok.Address}, tok.Symbol)
		// Lowercased for the same reason as the Aave reserve id: it is the key
		// the allowlist and the direct-RPC source match on.
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

// parseDecimal reads a GraphQL BigDecimal, which arrives as a JSON string.
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
