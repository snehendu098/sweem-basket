// Package source fetches yield venues from protocol subgraphs on The Graph.
package source

import (
	"context"
	"strings"
	"time"

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
	case v.APY <= 0 || v.APY > f.MaxAPY:
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

// Status reports the outcome of the last fetch for one protocol.
type Status struct {
	Protocol    string    `json:"protocol"`
	SubgraphID  string    `json:"subgraph_id"`
	OK          bool      `json:"ok"`
	Venues      int       `json:"venues"`
	Error       string    `json:"error,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastAttempt time.Time `json:"last_attempt"`
	// Unpriceable lists assets this adapter surfaced but had to drop because no
	// usable price existed, so /sources shows the gap instead of hiding it.
	Unpriceable []Unpriceable `json:"unpriceable,omitempty"`
}
