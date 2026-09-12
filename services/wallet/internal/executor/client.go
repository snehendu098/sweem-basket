package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type RouteRequest struct {
	ExecutionID   string  `json:"execution_id"`
	UserWallet    string  `json:"user_wallet"`
	PrivyDID      string  `json:"privy_did"`
	PrivyWalletID string  `json:"privy_wallet_id"`
	Chain         string  `json:"chain"`
	ChainID       int     `json:"chain_id"`
	Asset         string  `json:"asset"`
	AmountUSD     float64 `json:"amount_usd"`
	Action        string  `json:"action"`
	FromVenueID   string  `json:"from_venue_id,omitempty"`
	ToVenueID     string  `json:"to_venue_id,omitempty"`
	MaxSlippageBp int     `json:"max_slippage_bps"`
}

type Step struct {
	Step    int    `json:"step"`
	TxHash  string `json:"tx_hash"`
	Outcome string `json:"outcome"`
}

type RouteResponse struct {
	ExecutionID string `json:"execution_id"`
	TxHash      string `json:"tx_hash"`
	Status      string `json:"status"`
	Steps       []Step `json:"steps,omitempty"`
	Error       string `json:"error,omitempty"`
}

type SwapPath struct {
	ChainID int    `json:"chain_id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Hops    int    `json:"hops"`
}

type AllowedVenue struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	ChainID int    `json:"chain_id"`
	Symbol  string `json:"symbol"`
}

type Client struct {
	base string
	http *http.Client

	mu       sync.Mutex
	allow    map[string]AllowedVenue
	allowAt  time.Time
	swaps    []SwapPath
	swapsAt  time.Time
	allowTTL time.Duration
}

func New(baseURL string) *Client {
	return &Client{
		base:     baseURL,
		http:     &http.Client{Timeout: 60 * time.Second},
		allowTTL: 5 * time.Minute,
	}
}

func (c *Client) Allowlist(ctx context.Context) (map[string]AllowedVenue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.allow != nil && time.Since(c.allowAt) < c.allowTTL {
		return c.allow, nil
	}

	var body struct {
		Venues []AllowedVenue `json:"venues"`
	}
	if err := c.getJSON(ctx, "/venues", "allowlist", &body); err != nil {
		return nil, err
	}
	if len(body.Venues) == 0 {
		// Fail closed: an empty allowlist must not be read as "allow anything".
		return nil, errors.New("executor: allowlist is empty")
	}
	out := make(map[string]AllowedVenue, len(body.Venues))
	for _, v := range body.Venues {
		out[v.ID] = v
	}
	c.allow, c.allowAt = out, time.Now()
	return out, nil
}

func (c *Client) SwapPaths(ctx context.Context) ([]SwapPath, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.swaps != nil && time.Since(c.swapsAt) < c.allowTTL {
		return c.swaps, nil
	}
	var body struct {
		Paths []SwapPath `json:"paths"`
	}
	if err := c.getJSON(ctx, "/swaps", "swap paths", &body); err != nil {
		return nil, err
	}
	if len(body.Paths) == 0 {
		return nil, errors.New("executor: swap allowlist is empty")
	}
	c.swaps, c.swapsAt = body.Paths, time.Now()
	return c.swaps, nil
}

// ponytail: linear scan; index it if the allowlist grows past a screenful.
func HasSwapPath(paths []SwapPath, chainID int, from, to string) bool {
	for _, p := range paths {
		if p.ChainID == chainID && p.From == from && p.To == to {
			return true
		}
	}
	return false
}

func (c *Client) getJSON(ctx context.Context, path, label string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("executor: %s: %w", label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("executor: %s: status %d", label, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("executor: %s: decode: %w", label, err)
	}
	return nil
}

func (c *Client) Route(ctx context.Context, req RouteRequest) (RouteResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return RouteResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/route", bytes.NewReader(body))
	if err != nil {
		return RouteResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return RouteResponse{}, fmt.Errorf("executor: route: %w", err)
	}
	defer resp.Body.Close()

	var out RouteResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return RouteResponse{}, fmt.Errorf("executor: decode: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("executor: status %d: %s", resp.StatusCode, out.Error)
	}
	return out, nil
}

func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("executor: unhealthy: status %d", resp.StatusCode)
	}
	return nil
}
