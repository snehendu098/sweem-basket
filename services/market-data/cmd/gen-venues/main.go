// Command gen-venues writes the executor's venue allowlist from indexed data
// plus on-chain verification.
//
// Why this exists: the allowlist was hand-written, so it held a fraction of the
// venues that exist, and every venue missing from it is an asset a user cannot
// put in a basket. Hand-curation was the ceiling, not the security model.
//
// What it does NOT do is let the executor fetch its allowlist at runtime. The
// security property is that the set of addresses the executor will call cannot
// be influenced by the request path — or by a compromised indexer. This is a
// build-time tool: it writes a file, a human reads the diff, the file ships
// inside the executor image.
//
// A venue is emitted only when ALL of these hold:
//
//  1. encodable   — its protocol maps to a VenueKind the executor implements
//  2. priceable   — its underlying has a verified Chainlink feed on that chain
//  3. liquid      — it clears the TVL floor (enforced by the market-data filter)
//  4. verified    — symbol(), decimals() and a protocol identity check pass
//     on chain: Aave via the Pool's getReservesList(), Comet via
//     baseToken(), ERC-4626 via asset(), Moonwell via isMToken()
//     plus underlying()
//  5. id matches  — the emitted id is byte-for-byte what venue.MakeID produces,
//     asserted rather than assumed
//
// Usage (from the repo root, with the same .env the services use):
//
//	go run ./services/market-data/cmd/gen-venues -out executor/venues.json
//	go run ./services/market-data/cmd/gen-venues -dry-run   # print, write nothing
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

// entry is one allowlist row, matching the executor's `Venue` exactly.
type entry struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	ChainID       int    `json:"chain_id"`
	Target        string `json:"target"`
	Asset         string `json:"asset"`
	AssetDecimals int    `json:"asset_decimals"`
	Symbol        string `json:"symbol"`
}

// file is the on-disk shape: provenance plus the rows.
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

// skip records a candidate that did not make it, and why. The list is as
// useful as the output: it names exactly what a price feed or a new VenueKind
// would unlock.
type skip struct {
	ID     string
	Chain  string
	Reason string
}

// kinds maps a market-data project onto the executor's VenueKind. A protocol
// absent from here cannot be encoded, so its venues are skipped rather than
// emitted and refused later.
var kinds = map[string]string{
	"aave-v3":     "aave_v3",
	"compound-v3": "compound_v3",
	"morpho-blue": "erc4626",
	"moonwell":    "ctoken",
	// A hold venue has no protocol call at all: the position is the token in
	// the user's own wallet, so target IS the asset and the executor's
	// "target must not be the asset" rule explicitly exempts this kind.
	source.ProtocolHold: "hold",
}

func main() {
	out := flag.String("out", "executor/venues.json", "file to write")
	dryRun := flag.Bool("dry-run", false, "print the result, write nothing")
	// -1 means "use the chain's own rule", which is the only setting that keeps
	// the allowlist and the published venue set agreeing with each other.
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
				"verified: symbol/decimals plus a protocol identity check, on chain",
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

// forChain indexes one chain, then verifies every candidate on that chain.
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

	// The generator has to see exactly what the publisher publishes. Aave,
	// Compound and the hold venues are read straight off the chain now, so
	// drawing candidates from the subgraph alone would omit every hold venue
	// from the allowlist — measured correctly and then unroutable. A venue the
	// publisher serves but the generator omits cannot be routed to; a venue the
	// generator emits but the publisher never serves is dead weight. Same two
	// sources, same source.Reconcile, so the two cannot disagree about what a
	// venue is.
	live, err := source.NewRPC(c, node, filter, feed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN  %s: no direct rate source: %v\n", c.Label, err)
	}

	fmt.Fprintf(os.Stderr, "\n%s: min_tvl_usd=%.0f max_apy=%.0f allow_zero_apy=%v\n",
		c.Label, filter.MinTVLUsd, filter.MaxAPY, filter.AllowZeroAPY)
	indexed, err := src.Fetch(ctx)
	if err != nil {
		// Every adapter failing is a broken run, not an empty chain.
		return nil, nil, err
	}
	var direct []venue.Venue
	if live != nil {
		if direct, err = live.Fetch(ctx); err != nil {
			// One source down still leaves a usable allowlist from the other;
			// it is the union that would be wrong to fabricate.
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
	// A build-time tool can afford to be slow, and a public Base node cannot
	// afford a burst: a 429 that reads as a failed verification would quietly
	// shrink the allowlist.
	rpc.MinInterval = 120 * time.Millisecond
	var (
		out     []entry
		skipped []skip
	)
	for _, v := range candidates {
		kind, ok := kinds[v.Project]
		if !ok {
			skipped = append(skipped, skip{v.ID, c.Label, "no VenueKind encodes " + v.Project})
			continue
		}
		// Rule 2, checked explicitly rather than inherited: Aave prices some
		// reserves from its own oracle, so a venue can survive the pipeline
		// with no Chainlink feed of ours. Routing into one would mean holding a
		// position we cannot value.
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
		out = append(out, e)
	}
	return out, skipped, nil
}

// verify resolves a venue's target and underlying, confirms both on chain, and
// re-derives the id. Any failure returns an error and the venue is omitted —
// an unverified address is a fabricated one.
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
		// Nothing is ever called on a hold venue, so there is no target to
		// resolve and no identity check beyond the token answering symbol()
		// and decimals() below.
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
	// The executor matches a venue's symbol against the basket's asset name and
	// against the swap allowlist, and both of those spell a token the way the
	// token spells itself — swaps.json says "wstETH", not "WSTETH". So the
	// emitted symbol is the on-chain symbol() verbatim. Upper-casing it here is
	// what made every hold venue unroutable: the comparison is exact, so
	// "WSTETH" matches no swap path and no basket asset.
	//
	// Market-data's canonical UPPER-CASE form stays what it always was — an
	// internal lookup key for ResolveAsset, the Chainlink feed tables and the
	// apy:<chain>:<ASSET> zsets — and the two are reconciled here: the chain
	// must be reporting the token we indexed, case aside, or we have the wrong
	// contract.
	if strings.EqualFold(symbol, v.Asset) && symbol != v.Asset {
		// Both spellings reach the executor — this one as venues.json's symbol,
		// the index's as the basket asset — and it compares them with !=. The
		// chain wins, and the drift is named so the asset table can be fixed.
		fmt.Fprintf(os.Stderr, "WARN  %s: chain says %q, index says %q; emitting the chain's spelling\n",
			v.ID, symbol, v.Asset)
	}
	if !strings.EqualFold(symbol, v.Asset) {
		return entry{}, fmt.Errorf("underlying is %s on chain but %s in the index", symbol, v.Asset)
	}
	if target == underlying && kind != "hold" {
		return entry{}, fmt.Errorf("target equals the asset")
	}

	// Rule 5: routing keys on this id, and a mismatch is silent and miserable.
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

// aaveTarget resolves the Pool for a reserve and proves the reserve belongs to
// it. Aave's subgraph reserve id is `underlying + addressesProvider`, so the
// Pool is derived from the id's own provider half via getPool() — never
// reconstructed from a constant. The short id form (our Base Sepolia
// deployment) carries only the underlying, so its Pool comes from the subgraph
// and is then checked the same way.
func aaveTarget(ctx context.Context, rpc *source.Caller, c source.Chain, poolID string) (target, underlying string, err error) {
	raw := strings.TrimPrefix(strings.ToLower(poolID), "0x")
	switch len(raw) {
	case 80: // underlying + provider
		underlying, provider := "0x"+raw[:40], "0x"+raw[40:]
		target, err = rpc.Address(ctx, provider, source.SelGetPool)
		if err != nil {
			return "", "", fmt.Errorf("provider %s getPool(): %w", provider, err)
		}
		if err := aaveListsReserve(ctx, rpc, target, underlying); err != nil {
			return "", "", err
		}
		return target, underlying, nil
	case 40: // underlying only; the Pool must come from the index
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

// aaveListsReserve is the identity check: the Pool must actually list this
// reserve. It is what stops a plausible-looking address from being emitted.
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
