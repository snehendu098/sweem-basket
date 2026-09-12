package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/config"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/source"
)

var aavePools struct {
	sync.Mutex
	byChain map[int]map[string]string
}

func aavePool(ctx context.Context, c source.Chain, underlying string) (string, bool) {
	aavePools.Lock()
	defer aavePools.Unlock()
	if aavePools.byChain == nil {
		aavePools.byChain = map[int]map[string]string{}
	}
	pools, loaded := aavePools.byChain[c.ID]
	if !loaded {
		pools = fetchAavePools(ctx, c)
		aavePools.byChain[c.ID] = pools
	}
	p, ok := pools[strings.ToLower(underlying)]
	return p, ok
}

func fetchAavePools(ctx context.Context, c source.Chain) map[string]string {
	out := map[string]string{}
	id := source.SubgraphID("AAVE_V3", c)
	if id == "" {
		return out
	}
	body, _ := json.Marshal(map[string]string{
		"query": `{reserves(first: 500){ id underlyingAsset pool }}`,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		source.SubgraphURL(config.GetEnv("GRAPH_GATEWAY_URL", source.DefaultGatewayURL), id),
		bytes.NewReader(body))
	if err != nil {
		return out
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("GRAPH_API_KEY"))

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN  %s: aave pool lookup: %v\n", c.Label, err)
		return out
	}
	defer resp.Body.Close()

	var parsed struct {
		Data struct {
			Reserves []struct {
				UnderlyingAsset string `json:"underlyingAsset"`
				Pool            string `json:"pool"`
			} `json:"reserves"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		fmt.Fprintf(os.Stderr, "WARN  %s: aave pool lookup decode: %v\n", c.Label, err)
		return out
	}
	for _, r := range parsed.Data.Reserves {
		if r.Pool != "" {
			out[strings.ToLower(r.UnderlyingAsset)] = strings.ToLower(r.Pool)
		}
	}
	return out
}
