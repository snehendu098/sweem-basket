// Package marketdata is a read-only client for the market-data service,
// which serves normalized yield venues indexed from The Graph.
package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var ErrNoVenue = errors.New("marketdata: no venue for asset")

// Venue mirrors the market-data service's normalized venue schema.
type Venue struct {
	ID         string    `json:"id"`
	Chain      string    `json:"chain"`
	Project    string    `json:"project"`
	Symbol     string    `json:"symbol"`
	Asset      string    `json:"asset"`
	TVLUsd     float64   `json:"tvl_usd"`
	APY        float64   `json:"apy"`
	APYBase    float64   `json:"apy_base"`
	APYReward  float64   `json:"apy_reward"`
	Stablecoin bool      `json:"stablecoin"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Client struct {
	base string
	http *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		base: baseURL,
		http: &http.Client{Timeout: 5 * time.Second},
	}
}

// Best returns the highest-APY venue for an asset, subject to a TVL floor.
// This is the routing decision: where a basket's slice of an asset should go.
func (c *Client) Best(ctx context.Context, asset, chain string, minTVL float64) (Venue, error) {
	q := url.Values{"asset": {asset}, "chain": {chain}}
	if minTVL > 0 {
		q.Set("min_tvl", strconv.FormatFloat(minTVL, 'f', 0, 64))
	}
	var env struct {
		Data Venue `json:"data"`
	}
	err := c.get(ctx, "/venues/best?"+q.Encode(), &env)
	return env.Data, err
}

// Venues lists venues for an asset, best APY first.
func (c *Client) Venues(ctx context.Context, asset, chain string, limit int) ([]Venue, error) {
	q := url.Values{"chain": {chain}}
	if asset != "" {
		q.Set("asset", asset)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var env struct {
		Data struct {
			Venues []Venue `json:"venues"`
		} `json:"data"`
	}
	err := c.get(ctx, "/venues?"+q.Encode(), &env)
	return env.Data.Venues, err
}

// get unwraps the market-data service's {"data": …} envelope — callers pass a
// struct with a matching `data` field.
func (c *Client) get(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("marketdata: %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNoVenue
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("marketdata: %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}
