// Package executor is the client for the Rust executor service.
//
// The executor is the only component that signs and submits transactions.
// It accepts requests from this service only — nothing else may call it.
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

// RouteRequest asks the executor to move a user's funds into or between venues.
// The executor signs via the user's Privy delegated action; funds never leave
// the user's own embedded wallet.
type RouteRequest struct {
	ExecutionID   string `json:"execution_id"` // our idempotency key
	UserWallet    string `json:"user_wallet"`
	PrivyDID      string `json:"privy_did"`
	PrivyWalletID string `json:"privy_wallet_id"`
	Chain         string `json:"chain"`
	// ChainID is what the executor enforces against: every venue named here
	// must live on this chain. The label above is for humans and logs.
	ChainID       int     `json:"chain_id"`
	Asset         string  `json:"asset"`
	AmountUSD     float64 `json:"amount_usd"`
	Action        string  `json:"action"` // deposit | withdraw | rebalance
	FromVenueID   string  `json:"from_venue_id,omitempty"`
	ToVenueID     string  `json:"to_venue_id,omitempty"`
	MaxSlippageBp int     `json:"max_slippage_bps"`
}

// Step is one transaction in a multi-call sequence. Omitted entirely when the
// request was rejected before anything was submitted, so a nil Steps is normal.
type Step struct {
	Step    int    `json:"step"`
	TxHash  string `json:"tx_hash"`
	Outcome string `json:"outcome"` // confirmed | reverted | pending | submitted | failed
}

type RouteResponse struct {
	ExecutionID string `json:"execution_id"`
	TxHash      string `json:"tx_hash"`
	// Status is submitted | confirmed | failed | pending.
	//
	// pending means the receipt poll timed out, NOT that the transaction
	// failed: it is probably still in the mempool. Treating it as failed would
	// invite a second submission of money that already moved.
	Status string `json:"status"`
	Steps  []Step `json:"steps,omitempty"`
	Error  string `json:"error,omitempty"`
}

// SwapPath is one entry of the executor's swap allowlist: a pair it will
// route, and nothing about how. Hops is informational.
type SwapPath struct {
	ChainID int    `json:"chain_id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Hops    int    `json:"hops"`
}

// AllowedVenue is one entry of the executor's allowlist.
type AllowedVenue struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	ChainID int    `json:"chain_id"`
	Symbol  string `json:"symbol"`
}

type Client struct {
	base string
	http *http.Client

	// The allowlist only changes when the executor restarts, so it is cached.
	// It is never defaulted to "everything is allowed": a failed fetch is an
	// error the caller must surface, because routing without it would propose
	// venues the executor will refuse.
	mu       sync.Mutex
	allow    map[string]AllowedVenue
	allowAt  time.Time
	swaps    []SwapPath
	swapsAt  time.Time
	allowTTL time.Duration
}

func New(baseURL string) *Client {
	// Signing plus submission is slower than a normal API call; give it room.
	return &Client{
		base:     baseURL,
		http:     &http.Client{Timeout: 60 * time.Second},
		allowTTL: 5 * time.Minute,
	}
}

// Allowlist returns the venues the executor will actually transact with, keyed
// by venue id. This is the source of truth for what is routable: market-data
// indexes every venue it can see, including protocols the executor cannot
// encode calldata for.
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
		// An empty allowlist would silently make every basket unroutable. The
		// executor refuses to start in that state, so seeing it here means we
		// are talking to something unexpected.
		return nil, errors.New("executor: allowlist is empty")
	}
	out := make(map[string]AllowedVenue, len(body.Venues))
	for _, v := range body.Venues {
		out[v.ID] = v
	}
	c.allow, c.allowAt = out, time.Now()
	return out, nil
}

// SwapPaths returns the pairs the executor will swap. Same contract as
// Allowlist: cached for the same TTL, and a failed fetch is an error rather
// than "assume anything is swappable" — a leg routed without a path exists
// only to fail at submission, after the user has committed.
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
		// The executor refuses to start with an empty swaps.json, so this means
		// we are talking to something unexpected — not that nothing is routable.
		return nil, errors.New("executor: swap allowlist is empty")
	}
	c.swaps, c.swapsAt = body.Paths, time.Now()
	return c.swaps, nil
}

// HasSwapPath reports whether the executor will route from -> to on a chain.
// ponytail: linear scan; the allowlist is a handful of pairs, index it if it
// ever grows past a screenful.
func HasSwapPath(paths []SwapPath, chainID int, from, to string) bool {
	for _, p := range paths {
		if p.ChainID == chainID && p.From == from && p.To == to {
			return true
		}
	}
	return false
}

// getJSON fetches and decodes one read-only executor endpoint. label names the
// call in errors, which is all that differed between the two callers.
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
