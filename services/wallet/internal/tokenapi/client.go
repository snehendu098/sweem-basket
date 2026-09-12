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

const DefaultBaseURL = "https://token-api.thegraph.com"

var ErrNotConfigured = errors.New("tokenapi: TOKEN_API_JWT not set")

type Balance struct {
	Contract   string  `json:"contract"`
	Symbol     string  `json:"symbol"`
	Name       string  `json:"name"`
	Decimals   int     `json:"decimals"`
	Amount     string  `json:"amount"`
	Value      float64 `json:"value"`
	Network    string  `json:"network"`
	LastUpdate string  `json:"last_update"`
}

type Client struct {
	base string
	jwt  string
	http *http.Client
}

func New(baseURL, jwt string) *Client {
	if jwt == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{base: baseURL, jwt: jwt, http: &http.Client{Timeout: 8 * time.Second}}
}

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
