// Package rpc is a minimal Ethereum JSON-RPC client. One client serves both
// the gas pricing and the pending-execution sweeper, so both read the same
// node configured by BASE_RPC_URL.
package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Client struct {
	url  string
	http *http.Client
}

func New(url string) *Client {
	return &Client{url: url, http: &http.Client{Timeout: 10 * time.Second}}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

func (c *Client) do(ctx context.Context, method string, params []any, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("rpc %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rpc %s: status %d", method, resp.StatusCode)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("rpc %s: decode: %w", method, err)
	}
	if env.Error != nil {
		return fmt.Errorf("rpc %s: %w", method, env.Error)
	}
	return json.Unmarshal(env.Result, out)
}

// GasPriceWei returns a percentile-based estimate: the next block's base fee
// plus the median 50th-percentile priority tip over the last few blocks. Using
// eth_feeHistory rather than eth_gasPrice keeps one outlier block from skewing
// the number the breakeven is priced on.
func (c *Client) GasPriceWei(ctx context.Context) (*big.Int, error) {
	var out struct {
		BaseFeePerGas []string   `json:"baseFeePerGas"`
		Reward        [][]string `json:"reward"`
	}
	if err := c.do(ctx, "eth_feeHistory", []any{"0x5", "latest", []int{50}}, &out); err != nil {
		return nil, err
	}
	if len(out.BaseFeePerGas) == 0 {
		return nil, fmt.Errorf("rpc: eth_feeHistory returned no base fee")
	}
	// The last entry is the *next* block's base fee — the one we would pay.
	base, err := parseHexBig(out.BaseFeePerGas[len(out.BaseFeePerGas)-1])
	if err != nil {
		return nil, err
	}
	tips := make([]*big.Int, 0, len(out.Reward))
	for _, r := range out.Reward {
		if len(r) == 0 {
			continue
		}
		t, err := parseHexBig(r[0])
		if err != nil {
			return nil, err
		}
		tips = append(tips, t)
	}
	if len(tips) == 0 {
		return nil, fmt.Errorf("rpc: eth_feeHistory returned no priority fees")
	}
	sort.Slice(tips, func(i, j int) bool { return tips[i].Cmp(tips[j]) < 0 })
	return new(big.Int).Add(base, tips[len(tips)/2]), nil
}

// Call performs an eth_call against the latest block and returns the raw
// return data.
func (c *Client) Call(ctx context.Context, to, data string) ([]byte, error) {
	var out string
	if err := c.do(ctx, "eth_call", []any{map[string]string{"to": to, "data": data}, "latest"}, &out); err != nil {
		return nil, err
	}
	return hex.DecodeString(strings.TrimPrefix(out, "0x"))
}

// Receipt is the slice of a transaction receipt the sweeper needs.
type Receipt struct {
	Status      string `json:"status"` // 0x1 success, 0x0 reverted
	BlockNumber string `json:"blockNumber"`
	TxHash      string `json:"transactionHash"`
}

// Success reports whether the transaction executed without reverting.
func (r Receipt) Success() bool { return r.Status == "0x1" }

// TransactionReceipt returns nil, nil when the node has no receipt yet — the
// transaction is still unmined, which is not an error.
func (c *Client) TransactionReceipt(ctx context.Context, txHash string) (*Receipt, error) {
	var out *Receipt
	if err := c.do(ctx, "eth_getTransactionReceipt", []any{txHash}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func parseHexBig(s string) (*big.Int, error) {
	v, ok := new(big.Int).SetString(strings.TrimPrefix(s, "0x"), 16)
	if !ok {
		return nil, fmt.Errorf("rpc: bad hex quantity %q", s)
	}
	return v, nil
}
