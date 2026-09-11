// Package executor is the client for the Rust executor service.
//
// The executor is the only component that signs and submits transactions.
// It accepts requests from this service only — nothing else may call it.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// RouteRequest asks the executor to move a user's funds into or between venues.
// The executor signs via the user's Privy delegated action; funds never leave
// the user's own embedded wallet.
type RouteRequest struct {
	ExecutionID   string  `json:"execution_id"` // our idempotency key
	UserWallet    string  `json:"user_wallet"`
	PrivyDID      string  `json:"privy_did"`
	PrivyWalletID string  `json:"privy_wallet_id"`
	Chain         string  `json:"chain"`
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

type Client struct {
	base string
	http *http.Client
}

func New(baseURL string) *Client {
	// Signing plus submission is slower than a normal API call; give it room.
	return &Client{base: baseURL, http: &http.Client{Timeout: 60 * time.Second}}
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
