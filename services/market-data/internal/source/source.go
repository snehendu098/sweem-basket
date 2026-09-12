// Package source fetches yield venues from protocol subgraphs on The Graph.
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

// Source is a swappable provider of normalized venues.
type Source interface {
	Name() string
	Fetch(ctx context.Context) ([]venue.Venue, error)
}

// Filter holds the product rules for what counts as a usable venue.
//
// Exposure and IL risk are not fields here: every adapter maps only
// single-asset lending/vault positions, so "single exposure, ilRisk=no" is
// enforced structurally by the queries themselves rather than by a predicate.
type Filter struct {
	Chains    []string // allowed chains, case-insensitive
	MinTVLUsd float64
	MaxAPY    float64 // sanity cap: reward-spike artifacts
	// AllowZeroAPY keeps a venue that currently pays nothing.
	//
	// On mainnet a 0% supply rate means nobody is borrowing or something is
	// broken, and routing into it is pointless. On a testnet it means exactly
	// one thing — nobody borrows on a testnet — and dropping those hides
	// markets that work perfectly, which is the opposite of what a testnet is
	// for. Keyed on chain by FilterFor, never global and never per protocol.
	AllowZeroAPY bool
}

// Accept reports whether a mapped venue is usable.
func (f Filter) Accept(v venue.Venue) bool {
	switch {
	case !f.allowsChain(v.Chain):
		return false
	case v.Asset == "": // unresolved underlying: drop rather than guess
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

// FilterFor is the product rule for one chain. Thresholds live here rather
// than at each call site so the publisher, the venue generator and anything
// else asking "is this venue usable" cannot disagree — a venue the generator
// allowlists but the publisher drops is unroutable, and the reverse is worse.
//
// Overrides are <KEY>_<chainid>. The bare <KEY> applies to MAINNET ONLY: it is
// the production threshold, and letting it reach the testnet is how a
// mainnet-sized floor silently deletes a testnet's entire venue list — the
// thing this function exists to prevent. Set MIN_TVL_USD_84532 to move the
// testnet floor.
func FilterFor(c Chain) Filter {
	testnet := c.ID == chains.BaseSepolia
	f := Filter{
		Chains: []string{c.Label},
		// Base Sepolia's entire lending universe is worth less than one mainnet
		// pool, so a mainnet-sized floor drops every venue and reads as a broken
		// router rather than as a threshold doing its job.
		MinTVLUsd: envFloatForChain("MIN_TVL_USD", c, testnet, pick(testnet, 0, 5_000)),
		// Base Sepolia's Aave WETH reserve legitimately prints ~102% APY
		// (70.4% APR compounded); on mainnet a rate that high is an artifact.
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

// envFloatForChain prefers <key>_<chainid>. The shared <key> is consulted only
// for chains that are not the testnet; see FilterFor.
func envFloatForChain(key string, c Chain, testnet bool, def float64) float64 {
	if !testnet {
		def = config.GetEnvFloat(key, def)
	}
	return config.GetEnvFloat(fmt.Sprintf("%s_%d", key, c.ID), def)
}

// Status reports the outcome of the last fetch for one protocol.
type Status struct {
	Protocol string `json:"protocol"`
	// Source names the path that served this row — "thegraph:base" or
	// "rpc:base". Two sources now answer for the same protocol, so without it
	// /sources cannot say which one produced a venue, or which one is down.
	Source     string `json:"source"`
	SubgraphID string `json:"subgraph_id"`
	// Chain is what makes two rows for the same protocol legible: the same
	// adapter runs once per chain, and a mainnet failure is not a Sepolia one.
	Chain       string    `json:"chain"`
	ChainID     int       `json:"chain_id"`
	OK          bool      `json:"ok"`
	Venues      int       `json:"venues"`
	Error       string    `json:"error,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastAttempt time.Time `json:"last_attempt"`
	// Unpriceable lists assets this adapter surfaced but had to drop because no
	// usable price existed, so /sources shows the gap instead of hiding it.
	Unpriceable []Unpriceable `json:"unpriceable,omitempty"`
}
