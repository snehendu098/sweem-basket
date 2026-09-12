package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	HeaderSecret     = "X-Keeper-Secret"
	HeaderActingUser = "X-Acting-User"
)

type RebalanceResult struct {
	MovedLegs  int `json:"moved_legs"`
	FailedLegs int `json:"failed_legs"`
}

type Wallet struct {
	base   string
	secret string
	http   *http.Client
}

func NewWallet(baseURL, secret string) *Wallet {
	return &Wallet{
		base:   baseURL,
		secret: secret,
		http:   &http.Client{Timeout: 90 * time.Second},
	}
}

func (c *Wallet) Rebalance(ctx context.Context, basketID, privyDID string) (RebalanceResult, error) {
	url := fmt.Sprintf("%s/v1/baskets/%s/rebalance", c.base, basketID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return RebalanceResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSecret, c.secret)
	req.Header.Set(HeaderActingUser, privyDID)

	resp, err := c.http.Do(req)
	if err != nil {
		return RebalanceResult{}, fmt.Errorf("wallet: rebalance: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusMultiStatus {
		body := make([]byte, 512)
		n, _ := resp.Body.Read(body)
		return RebalanceResult{}, fmt.Errorf("wallet: rebalance: status %d: %s", resp.StatusCode, body[:n])
	}
	var out RebalanceResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return RebalanceResult{}, fmt.Errorf("wallet: decode: %w", err)
	}
	return out, nil
}
