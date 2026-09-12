package source

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
	"github.com/snehendu098/sweem-basket/internal/shared/config"

	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

type Source interface {
	Name() string
	Fetch(ctx context.Context) ([]venue.Venue, error)
}

type Filter struct {
	Chains       []string
	MinTVLUsd    float64
	MaxAPY       float64
	AllowZeroAPY bool
}

func (f Filter) Accept(v venue.Venue) bool {
	switch {
	case !f.allowsChain(v.Chain):
		return false
	case v.Asset == "":
		return false
	case v.PoolID == "" || v.Project == "":
		return false
	case v.TVLUsd < f.MinTVLUsd:
		return false
	case v.APY < 0 || v.APY > f.MaxAPY:
		return false
	case v.APY == 0 && !f.AllowZeroAPY:
		return false
	}
	return true
}

func (f Filter) allowsChain(chain string) bool {
	for _, c := range f.Chains {
		if strings.EqualFold(c, chain) {
			return true
		}
	}
	return false
}

func FilterFor(c Chain) Filter {
	testnet := c.ID == chains.BaseSepolia
	f := Filter{
		Chains:       []string{c.Label},
		MinTVLUsd:    envFloatForChain("MIN_TVL_USD", c, testnet, pick(testnet, 0, 5_000)),
		MaxAPY:       envFloatForChain("MAX_APY", c, testnet, pick(testnet, 1000, 100)),
		AllowZeroAPY: testnet,
	}
	return f
}

func pick(cond bool, a, b float64) float64 {
	if cond {
		return a
	}
	return b
}

func envFloatForChain(key string, c Chain, testnet bool, def float64) float64 {
	if !testnet {
		def = config.GetEnvFloat(key, def)
	}
	return config.GetEnvFloat(fmt.Sprintf("%s_%d", key, c.ID), def)
}

type Status struct {
	Protocol    string        `json:"protocol"`
	Source      string        `json:"source"`
	SubgraphID  string        `json:"subgraph_id"`
	Chain       string        `json:"chain"`
	ChainID     int           `json:"chain_id"`
	OK          bool          `json:"ok"`
	Venues      int           `json:"venues"`
	Error       string        `json:"error,omitempty"`
	LastSuccess time.Time     `json:"last_success,omitempty"`
	LastAttempt time.Time     `json:"last_attempt"`
	Unpriceable []Unpriceable `json:"unpriceable,omitempty"`
}
