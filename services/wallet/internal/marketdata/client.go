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

type Venue struct {
	ID           string    `json:"id"`
	Chain        string    `json:"chain"`
	Project      string    `json:"project"`
	Symbol       string    `json:"symbol"`
	Asset        string    `json:"asset"`
	TVLUsd       float64   `json:"tvl_usd"`
	APY          float64   `json:"apy"`
	APYBase      float64   `json:"apy_base"`
	APYReward    float64   `json:"apy_reward"`
	APYIntrinsic float64   `json:"apy_intrinsic"`
	Stablecoin   bool      `json:"stablecoin"`
	UpdatedAt    time.Time `json:"updated_at"`

	LiquidityUsd   float64 `json:"liquidity_usd"`
	LiquidityKnown bool    `json:"liquidity_known"`
	CollateralOnly bool    `json:"collateral_only"`
	NotRoutable    string  `json:"not_routable"`
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

func (c *Client) Venues(ctx context.Context, asset, chain string, minTVL float64, limit int) ([]Venue, error) {
	q := url.Values{"chain": {chain}}
	if asset != "" {
		q.Set("asset", asset)
	}
	if minTVL > 0 {
		q.Set("min_tvl", strconv.FormatFloat(minTVL, 'f', 0, 64))
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

type Asset struct {
	Asset    string  `json:"asset"`
	Chain    string  `json:"chain"`
	Family   string  `json:"family"`
	Venues   int     `json:"venues"`
	Routable int     `json:"routable_venues"`
	BestAPY  float64 `json:"best_apy"`
}

// Sorted by best APY, highest first, by the market-data service.
func (c *Client) Assets(ctx context.Context, chain string) ([]Asset, error) {
	var env struct {
		Data struct {
			Assets []Asset `json:"assets"`
		} `json:"data"`
	}
	err := c.get(ctx, "/assets?"+url.Values{"chain": {chain}}.Encode(), &env)
	return env.Data.Assets, err
}

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
