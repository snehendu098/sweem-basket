package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Headers the wallet service must accept for service-to-service calls. The
// keeper cannot mint a Privy access token for a user, so it authenticates as
// itself and names the user it is acting for.
const (
	HeaderSecret     = "X-Keeper-Secret"
	HeaderActingUser = "X-Acting-User"
)

// RebalanceResult is the wallet service's per-basket rebalance summary.
type RebalanceResult struct {
	MovedLegs  int `json:"moved_legs"`
	FailedLegs int `json:"failed_legs"`
}

// Wallet is the keeper's side of the rebalance call. It exists as an interface
// so tests and --dry-run can swap in a double; the keeper never signs anything
// and never calls the executor.
type Wallet struct {
	base   string
	secret string
	http   *http.Client
}

func NewWallet(baseURL, secret string) *Wallet {
	return &Wallet{
		base:   baseURL,
		secret: secret,
		// Rebalances fan out to the executor leg by leg; be patient.
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

// Rebalance asks the wallet service to rebalance one basket for one user.
// The wallet service re-checks delegation, drift and every other invariant —
// the keeper's decision is a trigger, not an authorization.
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

	// 207 means some legs moved and some did not — a partial success we still
	// want the counts from.
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
