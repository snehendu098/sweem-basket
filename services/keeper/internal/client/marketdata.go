// Package client holds the keeper's two outbound HTTP dependencies:
// market-data (read-only) and the wallet service (the only thing allowed to
// touch the executor).
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Venue mirrors the market-data service's normalized venue schema. Only the
// fields the keeper actually reasons about are kept.
type Venue struct {
	ID        string    `json:"id"`
	Chain     string    `json:"chain"`
	Project   string    `json:"project"`
	Asset     string    `json:"asset"`
	TVLUsd    float64   `json:"tvl_usd"`
	APY       float64   `json:"apy"`
	APYBase   float64   `json:"apy_base"`
	APYReward float64   `json:"apy_reward"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MarketData struct {
	base string
	http *http.Client
}

func NewMarketData(baseURL string) *MarketData {
	return &MarketData{base: baseURL, http: &http.Client{Timeout: 5 * time.Second}}
}

// Venues lists candidate venues for an asset above a TVL floor. The keeper
// takes the list rather than /venues/best because market-data ranks on raw
// APY, and the keeper ranks on reward-discounted APY — those disagree.
func (c *MarketData) Venues(ctx context.Context, asset, chain string, minTVL float64, limit int) ([]Venue, error) {
	q := url.Values{"asset": {asset}, "chain": {chain}, "limit": {strconv.Itoa(limit)}}
	if minTVL > 0 {
		q.Set("min_tvl", strconv.FormatFloat(minTVL, 'f', 0, 64))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/venues?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("marketdata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("marketdata: status %d", resp.StatusCode)
	}
	// The market-data service wraps every body in a {"data": …} envelope.
	var env struct {
		Data struct {
			Venues []Venue `json:"venues"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("marketdata: decode: %w", err)
	}
	return env.Data.Venues, nil
}
