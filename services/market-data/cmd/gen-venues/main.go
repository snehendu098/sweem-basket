package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
	"github.com/snehendu098/sweem-basket/internal/shared/config"
	"github.com/snehendu098/sweem-basket/internal/shared/prices"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/source"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

type entry struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	ChainID       int    `json:"chain_id"`
	Target        string `json:"target"`
	Asset         string `json:"asset"`
	AssetDecimals int    `json:"asset_decimals"`
	Symbol        string `json:"symbol"`
}

type file struct {
	Generated generated `json:"_generated"`
	Venues    []entry   `json:"venues"`
}

type generated struct {
	By    string   `json:"by"`
	At    string   `json:"at"`
	How   string   `json:"how"`
	Note  string   `json:"note"`
	Rules []string `json:"rules"`
}

type skip struct {
	ID     string
	Chain  string
	Reason string
}

var kinds = map[string]string{
	"aave-v3":           "aave_v3",
	"compound-v3":       "compound_v3",
	"morpho-blue":       "erc4626",
	"moonwell":          "ctoken",
	source.ProtocolHold: "hold",
}

const fundingAsset = "USDC"

// A venue whose asset cannot be swapped back is a place funds go and cannot
// leave. Withdrawals are denominated in USDC, so this is a hard gate, not a
// preference.
func exitable(entries []entry, swapsPath string) (kept []entry, stranded []entry) {
	raw, err := os.ReadFile(swapsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL cannot read %s: %v\n", swapsPath, err)
		os.Exit(1)
	}
	var doc struct {
		Paths []struct {
			ChainID int    `json:"chain_id"`
			From    string `json:"from"`
			To      string `json:"to"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL cannot parse %s: %v\n", swapsPath, err)
		os.Exit(1)
	}
	exits := map[string]bool{}
	for _, p := range doc.Paths {
		if p.To == fundingAsset {
			exits[fmt.Sprintf("%d:%s", p.ChainID, p.From)] = true
		}
	}
	for _, e := range entries {
		if e.Symbol == fundingAsset || exits[fmt.Sprintf("%d:%s", e.ChainID, e.Symbol)] {
			kept = append(kept, e)
			continue
		}
		stranded = append(stranded, e)
	}
	return kept, stranded
}

func main() {
	out := flag.String("out", "executor/venues.json", "file to write")
	dryRun := flag.Bool("dry-run", false, "print the result, write nothing")
	swaps := flag.String("swaps", "executor/swaps.json", "swap allowlist, read to check every venue can be exited")
	minTVL := flag.Float64("min-tvl", -1, "override the chain's USD TVL floor")
	flag.Parse()

	config.LoadDotEnv(config.GetEnv("ENV_FILE", ".env"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var (
		entries []entry
		skipped []skip
	)
	for _, chainID := range chains.Supported() {
		label, _ := chains.Label(chainID)
		c := source.Chain{Label: label, ID: chainID}
		es, sk, err := forChain(ctx, c, *minTVL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL %s: %v\n", label, err)
			os.Exit(1)
		}
		entries = append(entries, es...)
		skipped = append(skipped, sk...)
	}

	entries, stranded := exitable(entries, *swaps)
	for _, v := range stranded {
		label, _ := chains.Label(v.ChainID)
		skipped = append(skipped, skip{
			ID:     v.ID,
			Chain:  label,
			Reason: "no allowlisted swap path back to " + fundingAsset,
		})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].ID < skipped[j].ID })

	report(entries, skipped)
	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "refusing to write an empty allowlist: the executor would refuse every request")
		os.Exit(1)
	}

	body, err := json.MarshalIndent(file{
		Generated: generated{
			By:   "services/market-data/cmd/gen-venues",
			At:   time.Now().UTC().Format(time.RFC3339),
			How:  "go run ./services/market-data/cmd/gen-venues -out executor/venues.json",
			Note: "GENERATED — do not edit by hand; re-run the generator or the next run drops your change.",
			Rules: []string{
				"encodable: protocol maps to a VenueKind the executor implements",
				"priceable: underlying has a verified Chainlink feed on that chain",
				"liquid: clears the TVL floor",
				"withdrawable: clears the MIN_LIQUIDITY_USD floor on measured, live withdrawable liquidity",
				"verified: symbol/decimals plus a protocol identity check, on chain",
				"familied: a human has decided the asset's substitution family (or that it has none)",
				"exitable: an allowlisted swap path returns the asset to " + fundingAsset,
				"id equals venue.MakeID(chain, project, pool)",
			},
		},
		Venues: entries,
	}, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	body = append(body, '\n')

	if *dryRun {
		fmt.Println(string(body))
		return
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d venues)\n", *out, len(entries))
}

func forChain(ctx context.Context, c source.Chain, minTVLOverride float64) ([]entry, []skip, error) {
	filter := source.FilterFor(c)
	if minTVLOverride >= 0 {
		filter.MinTVLUsd = minTVLOverride
	}
	node := prices.NewHTTPRPC(rpcURL(c.ID))
	feed := prices.New(node, c.ID, time.Hour, time.Minute)
	src, err := source.NewGraph(
		c,
		config.GetEnv("GRAPH_GATEWAY_URL", source.DefaultGatewayURL),
		os.Getenv("GRAPH_API_KEY"),
		filter,
		feed,
		source.Adapters(c)...,
	)
	if err != nil {
		return nil, nil, err
	}

	live, err := source.NewRPC(c, node, filter, feed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN  %s: no direct rate source: %v\n", c.Label, err)
	}

	fmt.Fprintf(os.Stderr, "\n%s: min_tvl_usd=%.0f min_liquidity_usd=%.0f max_apy=%.0f allow_zero_apy=%v\n",
		c.Label, filter.MinTVLUsd, filter.MinLiquidityUsd, filter.MaxAPY, filter.AllowZeroAPY)
	indexed, err := src.Fetch(ctx)
	if err != nil {
		return nil, nil, err
	}
	var direct []venue.Venue
	if live != nil {
		if direct, err = live.Fetch(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "WARN  %s: direct rate source failed: %v\n", c.Label, err)
		}
	}
	statuses := src.Status()
	if live != nil {
		statuses = append(statuses, live.Status()...)
	}
	for _, st := range statuses {
		if !st.OK {
			fmt.Fprintf(os.Stderr, "WARN  %s/%s [%s]: %s\n", c.Label, st.Protocol, st.Source, st.Error)
		}
	}
	candidates := source.Reconcile(indexed, direct, source.DefaultRateTolerance)
	fmt.Fprintf(os.Stderr, "%s: %d candidates (%d indexed, %d live)\n",
		c.Label, len(candidates), len(indexed), len(direct))

	chainFeeds, ratios := prices.FeedsFor(c.ID)
	rpc := source.NewCaller(node)
	rpc.MinInterval = 120 * time.Millisecond
	var (
		out     []entry
		skipped []skip
	)
	for _, v := range candidates {
		if !v.Routable() {
			skipped = append(skipped, skip{v.ID, c.Label, v.NotRoutable})
			continue
		}
		kind, ok := kinds[v.Project]
		if !ok {
			skipped = append(skipped, skip{v.ID, c.Label, "no VenueKind encodes " + v.Project})
			continue
		}
		asset := strings.ToUpper(v.Asset)
		if _, direct := chainFeeds[asset]; !direct {
			if _, composed := ratios[asset]; !composed {
				skipped = append(skipped, skip{v.ID, c.Label, "no Chainlink feed for " + asset + " on this chain"})
				continue
			}
		}

		e, err := verify(ctx, rpc, c, v, kind)
		if err != nil {
			skipped = append(skipped, skip{v.ID, c.Label, err.Error()})
			continue
		}
		if _, decided := source.FamilyOfAddress(e.Asset); !decided {
			return nil, nil, fmt.Errorf("%s: no asset family decided for %s (%s); add it to assetFamilies in internal/source/assets.go before it can be routed",
				e.ID, e.Symbol, e.Asset)
		}
		out = append(out, e)
	}
	return out, skipped, nil
}

func verify(ctx context.Context, rpc *source.Caller, c source.Chain, v venue.Venue, kind string) (entry, error) {
	var target, underlying string
	var err error

	switch kind {
	case "aave_v3":
		target, underlying, err = aaveTarget(ctx, rpc, c, v.PoolID)
	case "compound_v3":
		target = v.PoolID
		underlying, err = rpc.Address(ctx, target, source.SelBaseToken)
	case "erc4626":
		target = v.PoolID
		underlying, err = rpc.Address(ctx, target, source.SelAsset)
	case "hold":
		target, underlying = v.PoolID, v.PoolID
	case "ctoken":
		target = v.PoolID
		var isM bool
		if isM, err = rpc.Bool(ctx, target, source.SelIsMToken); err == nil && !isM {
			err = fmt.Errorf("isMToken() is false: not a Moonwell market")
		}
		if err == nil {
			underlying, err = rpc.Address(ctx, target, source.SelUnderlying)
		}
	default:
		return entry{}, fmt.Errorf("unhandled kind %s", kind)
	}
	if err != nil {
		return entry{}, fmt.Errorf("identity check failed: %w", err)
	}

	symbol, err := rpc.Text(ctx, underlying, source.SelSymbol)
	if err != nil {
		return entry{}, fmt.Errorf("underlying symbol(): %w", err)
	}
	decimals, err := rpc.Uint8(ctx, underlying, source.SelDecimals)
	if err != nil {
		return entry{}, fmt.Errorf("underlying decimals(): %w", err)
	}
	if strings.EqualFold(symbol, v.Asset) && symbol != v.Asset {
		fmt.Fprintf(os.Stderr, "WARN  %s: chain says %q, index says %q; emitting the chain's spelling\n",
			v.ID, symbol, v.Asset)
	}
	if !strings.EqualFold(symbol, v.Asset) {
		return entry{}, fmt.Errorf("underlying is %s on chain but %s in the index", symbol, v.Asset)
	}
	if target == underlying && kind != "hold" {
		return entry{}, fmt.Errorf("target equals the asset")
	}

	id := venue.MakeID(c.Label, v.Project, v.PoolID)
	if id != v.ID {
		return entry{}, fmt.Errorf("id %s does not match MakeID output %s", v.ID, id)
	}
	if !strings.HasPrefix(id, c.Label+":") {
		return entry{}, fmt.Errorf("id %s is not labelled %s", id, c.Label)
	}

	return entry{
		ID:            id,
		Kind:          kind,
		ChainID:       c.ID,
		Target:        strings.ToLower(target),
		Asset:         strings.ToLower(underlying),
		AssetDecimals: decimals,
		Symbol:        symbol,
	}, nil
}

func aaveTarget(ctx context.Context, rpc *source.Caller, c source.Chain, poolID string) (target, underlying string, err error) {
	raw := strings.TrimPrefix(strings.ToLower(poolID), "0x")
	switch len(raw) {
	case 80:
		underlying, provider := "0x"+raw[:40], "0x"+raw[40:]
		target, err = rpc.Address(ctx, provider, source.SelGetPool)
		if err != nil {
			return "", "", fmt.Errorf("provider %s getPool(): %w", provider, err)
		}
		if err := aaveListsReserve(ctx, rpc, target, underlying); err != nil {
			return "", "", err
		}
		return target, underlying, nil
	case 40:
		underlying = "0x" + raw
		pool, ok := aavePool(ctx, c, underlying)
		if !ok {
			return "", "", fmt.Errorf("cannot resolve the Aave Pool for %s", underlying)
		}
		if err := aaveListsReserve(ctx, rpc, pool, underlying); err != nil {
			return "", "", err
		}
		return pool, underlying, nil
	default:
		return "", "", fmt.Errorf("unrecognised aave reserve id shape %q", poolID)
	}
}

func aaveListsReserve(ctx context.Context, rpc *source.Caller, pool, underlying string) error {
	list, err := rpc.AddressList(ctx, pool, source.SelGetReservesList)
	if err != nil {
		return fmt.Errorf("pool %s getReservesList(): %w", pool, err)
	}
	for _, a := range list {
		if strings.EqualFold(a, underlying) {
			return nil
		}
	}
	return fmt.Errorf("pool %s does not list reserve %s", pool, underlying)
}

func rpcURL(chainID int) string {
	def := "https://mainnet.base.org"
	if chainID == chains.BaseSepolia {
		def = "https://sepolia.base.org"
	}
	return config.GetEnv(fmt.Sprintf("BASE_RPC_URL_%d", chainID), def)
}

func report(entries []entry, skipped []skip) {
	byChain := map[int]int{}
	for _, e := range entries {
		byChain[e.ChainID]++
	}
	fmt.Fprintln(os.Stderr, "\n== emitted ==")
	for _, id := range chains.Supported() {
		label, _ := chains.Label(id)
		fmt.Fprintf(os.Stderr, "%-14s %d venues\n", label, byChain[id])
	}
	for _, e := range entries {
		fmt.Fprintf(os.Stderr, "  %-12s %-6s %s\n", e.Kind, e.Symbol, e.ID)
	}
	fmt.Fprintf(os.Stderr, "\n== skipped (%d) ==\n", len(skipped))
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "  %-60s %s\n", s.ID, s.Reason)
	}
}
