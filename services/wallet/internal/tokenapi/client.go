// Package tokenapi is a read-only client for The Graph's Token API, which
// serves indexed onchain ERC-20 balances.
//
// Onchain balances are the truth; the positions table is only our bookkeeping.
// The portfolio endpoint compares the two.
package tokenapi

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

// DefaultBaseURL is The Graph's hosted Token API.
const DefaultBaseURL = "https://token-api.thegraph.com"

// ErrNotConfigured is returned when no JWT was supplied. Callers treat this as
// "skip the onchain view", never as a failure.
var ErrNotConfigured = errors.New("tokenapi: TOKEN_API_JWT not set")

// Balance is one ERC-20 holding. Field names follow the API's
// GET /v1/evm/balances response items.
type Balance struct {
	Contract   string  `json:"contract"`
	Symbol     string  `json:"symbol"`
	Name       string  `json:"name"`
	Decimals   int     `json:"decimals"`
	Amount     string  `json:"amount"` // raw integer, as a string
	Value      float64 `json:"value"`  // amount scaled by decimals
	Network    string  `json:"network"`
	LastUpdate string  `json:"last_update"`
}

type Client struct {
	base string
	jwt  string
	http *http.Client
}

// New returns nil when jwt is empty. A nil *Client is safe to call: every
// method returns ErrNotConfigured, so the demo runs without the key.
func New(baseURL, jwt string) *Client {
	if jwt == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{base: baseURL, jwt: jwt, http: &http.Client{Timeout: 8 * time.Second}}
}

// Balances lists a wallet's ERC-20 balances on one network ("base", "mainnet", …).
func (c *Client) Balances(ctx context.Context, address, network string, limit int) ([]Balance, error) {
	if c == nil {
		return nil, ErrNotConfigured
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := url.Values{
		"address": {address},
		"network": {network},
		"limit":   {strconv.Itoa(limit)},
	}
	path := "/v1/evm/balances?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.jwt)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tokenapi: balances: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tokenapi: balances: status %d", resp.StatusCode)
	}

	var env struct {
		Data []Balance `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("tokenapi: decode: %w", err)
	}
	return env.Data, nil
}
