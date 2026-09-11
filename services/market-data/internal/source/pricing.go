package source

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/snehendu098/sweem-basket/internal/shared/prices"
)

// PriceFeed is the USD price source every adapter shares — in production the
// Chainlink client in internal/shared/prices, in tests a stub. Nothing here
// falls back to a peg: an asset we cannot price is an asset we do not route.
type PriceFeed interface {
	USD(ctx context.Context, symbol string) (prices.Price, error)
}

// Pricer is the one USD conversion path for all adapters, scoped to a single
// fetch. It carries the fetch context so Map stays a pure schema mapper with no
// transport arguments, and records every asset it could not price so
// GET /sources can show which assets are missing feeds instead of them just
// vanishing from the venue list.
type Pricer struct {
	ctx  context.Context
	feed PriceFeed

	mu     sync.Mutex
	missed map[string]string // asset -> reason
}

func NewPricer(ctx context.Context, feed PriceFeed) *Pricer {
	return &Pricer{ctx: ctx, feed: feed, missed: map[string]string{}}
}

// USD returns the price of one unit of asset. A missing feed, a stale round or
// an RPC failure all return ok=false — never a default, never $1, never the
// price of a similar asset. The caller drops the venue.
func (p *Pricer) USD(asset string) (float64, bool) {
	if asset == "" {
		return 0, false
	}
	if p == nil || p.feed == nil {
		p.record(asset, "no price feed configured")
		return 0, false
	}
	price, err := p.feed.USD(p.ctx, asset)
	if err != nil {
		p.record(asset, err.Error())
		return 0, false
	}
	if price.USD <= 0 {
		p.record(asset, "non-positive price")
		return 0, false
	}
	return price.USD, true
}

func (p *Pricer) record(asset, reason string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	_, seen := p.missed[asset]
	p.missed[asset] = reason
	p.mu.Unlock()
	if !seen {
		slog.Warn("asset unpriceable, venues dropped", "asset", asset, "reason", reason)
	}
}

// Unpriceable lists the assets this fetch had to drop, with the reason.
func (p *Pricer) Unpriceable() []Unpriceable {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Unpriceable, 0, len(p.missed))
	for a, r := range p.missed {
		out = append(out, Unpriceable{Asset: a, Reason: r})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Asset < out[j].Asset })
	return out
}

// Unpriceable is one asset dropped for want of a usable price.
type Unpriceable struct {
	Asset  string `json:"asset"`
	Reason string `json:"reason"`
}
