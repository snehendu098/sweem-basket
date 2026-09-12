package source

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/snehendu098/sweem-basket/internal/shared/prices"
)

type PriceFeed interface {
	USD(ctx context.Context, symbol string) (prices.Price, error)
}

type Pricer struct {
	ctx  context.Context
	feed PriceFeed

	mu     sync.Mutex
	missed map[string]string
}

func NewPricer(ctx context.Context, feed PriceFeed) *Pricer {
	return &Pricer{ctx: ctx, feed: feed, missed: map[string]string{}}
}

// Fail closed: an unpriceable asset returns ok=false and the caller drops the
// venue. Never $1, never a similar asset's price.
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

type Unpriceable struct {
	Asset  string `json:"asset"`
	Reason string `json:"reason"`
}
