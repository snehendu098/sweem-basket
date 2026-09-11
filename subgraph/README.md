# subgraph/

Three subgraphs for **Base Sepolia (chain 84532)**, network identifier
`base-sepolia`. Together they are the data layer under
`services/market-data` — the normalizing service that turns three protocols'
incompatible rate conventions into one `Venue` model.

**None of these exist upstream.** Aave publishes mainnet manifests only.
Compound publishes no Base Sepolia deployment. Morpho's own subgraph was
archived in March 2025. On mainnet the router composes subgraphs other people
wrote; here the ones it composes had to be written.

| Directory | Protocol | Contract | Deploy block |
|---|---|---|---|
| `aave-v3-base-sepolia/` | Aave V3 | Pool `0x07eA79F68B2B3df564D0A34F8e19D9B1e339814b` | 6719154 |
| | | PoolConfigurator `0x347Ae6820F48e9Dd563235742d89FAef6ffCaA72` | 6719154 |
| `compound-v3-base-sepolia/` | Compound III | Comet cUSDCv3 `0x571621Ce60Cebb0c1D442B5afb38B1663C6Bf017` | 7646646 |
| `morpho-blue-base/` | MetaMorpho | v1.0 factory `0xA9c3D3a366466Fa809d1Ae982Fb2c46E5fC41101` | 9315415 |

Every deploy block was found by binary search on `eth_getCode` against
`https://sepolia.base.org` and independently reproduced on
`https://base-sepolia.drpc.org`: block `N-1` returns `0x`, block `N` returns
bytecode.

## What each one indexes, and why it has to

Each protocol hides its yield somewhere different, which is the whole reason a
normalizing layer exists:

| | Where the rate lives | What the subgraph stores | `apy.go` conversion |
|---|---|---|---|
| **Aave V3** | on the `ReserveDataUpdated` event | `liquidityRate`, an Aave **ray** (1e27 == 100% APR) | `RayRateToAPY` |
| **Compound III** | **nowhere** — no rate event exists | `getSupplyRate(getUtilization())`, a **per-second** rate scaled 1e18, plus a derived `supplyApr` decimal fraction | `APRToAPY(netSupplyApr)` |
| **MetaMorpho** | **nowhere** — no rate field exists | `sharePriceScaled = totalAssets * 1e36 / totalSupply`, sampled hourly | `SharePriceGrowthToAPY` |

All three also emit an hourly snapshot series, so a rate history exists for a
chain where none did before.

## Deploy sequence

Order does not matter — they are independent. Do all three.

```sh
# once per machine, key from https://thegraph.com/studio/
bunx graph auth <DEPLOY_KEY>
```

Then, from each directory in turn:

```sh
cd subgraph/aave-v3-base-sepolia
bun install && bunx graph codegen && bunx graph build --network base-sepolia
bunx graph deploy aave-v-3-base-sepolia --network base-sepolia

cd ../compound-v3-base-sepolia
bun install && bunx graph codegen && bunx graph build --network base-sepolia
bunx graph deploy compound-v-3-base-sepolia --network base-sepolia

cd ../morpho-blue-base
bun install && bunx graph codegen && bunx graph build --network base-sepolia
bunx graph deploy morpho-blue-base --network base-sepolia
```

`graph deploy` prompts for a version label (`v0.0.1`) and prints the Studio
query URL when it finishes. **Substitute your own Studio slug** for the names
above if the subgraph you created in Studio is named differently — the argument
to `graph deploy` is the Studio slug, not the directory name.

Publishing to the decentralized network (optional, from the Studio UI) is what
produces a `Qm…`-style **subgraph ID**. Until then, the Studio dev endpoint is
addressed by *slug and version*, not by ID — see below.

## Handoff: which env var each deployment feeds

This is the part that wastes an hour if it is wrong. Each deployed subgraph maps
to exactly one env var read by `services/market-data`:

| Directory | Studio slug | Env var | Read by |
|---|---|---|---|
| `aave-v3-base-sepolia/` | `aave-v3-base-sepolia` | `AAVE_V3_SUBGRAPH_ID` | `internal/source/aave_v3.go` |
| `compound-v3-base-sepolia/` | `compound-v3-base-sepolia` | `COMPOUND_V3_SUBGRAPH_ID` | `internal/source/compound_v3.go` |
| `morpho-blue-base/` | `morpho-blue-base` | `MORPHO_SUBGRAPH_ID` | `internal/source/morpho.go` |

`aave_v3.go` and `compound_v3.go` fall back to a hardcoded **Base mainnet**
subgraph ID when their env var is unset (`AaveV3BaseSubgraphID`,
`CompoundV3BaseSubgraphID`). On Base Sepolia those defaults are wrong — they
silently return mainnet venues rather than failing. `morpho.go` defaults to `""`
and disables itself instead. **Set all three**, or the router quotes mainnet
rates for a testnet deployment.

## Endpoint shape: Studio vs. the decentralized gateway

`services/market-data/internal/source/graph.go` builds its request URL as a
plain concatenation:

```go
url := g.Gateway + "/" + a.SubgraphID()      // graph.go:173
const DefaultGatewayURL = "https://gateway.thegraph.com/api/subgraphs/id"
```

The two endpoint shapes are:

```
decentralized:  https://gateway.thegraph.com/api/subgraphs/id/<SUBGRAPH_ID>
Studio (dev):   https://api.studio.thegraph.com/query/<STUDIO_ACCOUNT_ID>/<SLUG>/<VERSION>
```

Studio has **three** path segments after the host prefix where the gateway has
one, and it is addressed by slug + version rather than by a subgraph ID. Two
consequences for the Go side:

1. **It already works without a code change**, by splitting the Studio URL at
   the account id:

   ```sh
   GRAPH_GATEWAY_URL=https://api.studio.thegraph.com/query/<STUDIO_ACCOUNT_ID>
   AAVE_V3_SUBGRAPH_ID=aave-v3-base-sepolia/version/v0.0.1
   COMPOUND_V3_SUBGRAPH_ID=compound-v3-base-sepolia/version/v0.0.1
   MORPHO_SUBGRAPH_ID=morpho-blue-base/version/v0.0.1
   ```

   The concatenation reproduces the Studio URL exactly. But `GRAPH_GATEWAY_URL`
   is global, so **all three must be on the same backend** — you cannot mix a
   Studio Aave with a gateway Morpho this way.

2. If mixing is needed, `SubgraphID()` should be allowed to be a full URL and
   `graph.go:173` should use it directly when it starts with `http`. That is a
   two-line change in `graph.go`; it is flagged here, not made — the Go tree is
   owned by another agent.

Studio dev endpoints do **not** require `GRAPH_API_KEY`; they ignore the
`Authorization` header the client already sends, so nothing breaks. The
decentralized gateway does require it.

## Build verification

`graph codegen && graph build --network base-sepolia` passes for all three. That
is the bar before deploying; run it after any schema or mapping edit.

## Conventions shared by all three

- **AssemblyScript, not TypeScript.** No closures, no `any`, mandatory null
  check on every entity load.
- **`try_` for every contract call.** One reverting reserve, paused Comet or
  broken vault must never halt indexing for everything else.
- **No USD pricing invented in the subgraph** beyond what the protocol's own
  oracle already reports. The consumer joins Chainlink prices; a subgraph that
  guesses a price misroutes money.
- **Raw units on the wire.** Totals are raw token units plus a `decimals` field;
  the consumer scales. Rates keep each protocol's native scale (ray,
  per-second-1e18, share price) so no precision is lost before `apy.go` sees it.
- **Hourly, event-driven snapshots** keyed `<entity>-<hourIndex>`, each carrying
  the real `timestamp` of the last sample folded in — so a consumer computes
  elapsed time from the samples it actually received rather than assuming the
  nominal spacing. A block handler on every 2-second Base block would be
  prohibitive; per-event rows would grow with traffic instead of with time.

## License

MIT

## Studio slugs differ from directory names

Subgraph Studio slugifies `v3` into `v-3`, so the deploy target is not the
directory name. Deploying with the directory name fails with the unhelpful
`Subgraph not found`.

| Directory | Studio slug |
|---|---|
| `aave-v3-base-sepolia` | `aave-v-3-base-sepolia` |
| `compound-v3-base-sepolia` | `compound-v-3-base-sepolia` |
| `morpho-blue-base` | `morpho-blue-base` (unchanged, no digit) |

Deployed under Studio account `1759980`, so the query endpoints are
`https://api.studio.thegraph.com/query/1759980/<slug>/v0.0.1`.
