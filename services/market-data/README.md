# market-data

Answers one question for the rest of the protocol: **where is the best yield for token X right now?**

Two binaries, one Redis, **two data sources** (see below):

- `cmd/publisher` — polls both sources on an interval, normalizes every protocol's
  rate convention into one `Venue` schema, reconciles the two views, rewrites Redis.
- `cmd/api` — read-only HTTP API over that Redis state. The wallet service calls
  `/venues/best` to decide where to route a deposit.

## Two sources, split by what the protocol publishes

```
spot rates       eth_call    Aave V3, Compound III, LST/sUSDS rates   instant, no sync
derived rates    subgraph    Morpho share prices, everything else     needs history
```

A subgraph has to replay chain history before it can answer. A freshly deployed
mainnet subgraph is days from current, and for the whole of that time it reports
either nothing or a stale rate — while the same numbers are one `eth_call` away,
live. So the rule is not "prefer one source", it is **use the source that can
actually measure the number**:

| Protocol | Source | Why |
|---|---|---|
| Aave V3 | `RPCSource` (+ subgraph) | `getReserveData().currentLiquidityRate` is the rate, right now |
| Compound III | `RPCSource` (+ subgraph) | `getSupplyRate(getUtilization())` is the rate, right now |
| `hold` (wstETH, cbETH, weETH, wrsETH, sUSDS) | `RPCSource` (+ subgraph) | Chainlink keeps its past rounds **on chain**, so the drift is readable without indexing; Sky publishes `ssr` outright |
| Morpho Blue | subgraph only | a vault's yield exists only as share-price drift between two indexed snapshots |
| Moonwell | subgraph only | no direct reader written; the subgraph is current |

The subgraph stays for everything either way: it is a deliverable in its own
right, and it is the only source for Morpho and for any protocol we have not
written a direct reader for.

### Reconciling the two

`internal/source/reconcile.go`. Each APY component is taken from whichever source
can measure it:

| Leg | Source | Why |
|---|---|---|
| `APYBase` | RPC | the rate the contract is paying this second |
| `APYReward` | subgraph | COMP emissions need a COMP/USD feed that does not exist on Base |
| `APYIntrinsic` | RPC, else subgraph | the oracle's own round history gives it instantly |

Where both sources produce a venue and their base rates differ by more than
`RATE_DISAGREEMENT_TOLERANCE` (0.25 percentage points), the gap is logged at warn
with both numbers. That is a genuinely useful signal rather than noise: it means
the subgraph is lagging, or one of the unit conversions is wrong — it is how a
repeat of the per-block/per-second mistake surfaces in one cycle instead of after
a rebalance.

Venue ids must be byte-identical across the two sources or the executor allowlist
stops matching one of them. Both lower-case the pool id, and `Reconcile` folds the
upstream Aave `underlying+addressesProvider` id shape onto the short `underlying`
form the allowlist holds. `TestVenueIDMatchesSubgraphSource` asserts it.

### Reading a rate that is not there

`RPCSource` never turns a failed read into a rate. A reverting call, a
rate-limited node, a frozen/paused/inactive Aave reserve, a Comet whose `symbol()`
does not match the table, an exchange-rate feed that did not move, an asset with
no Chainlink price — every one of them omits the venue with a logged reason.
Zero would read as "this market pays nothing" and route money in exactly the
wrong direction.

### Public nodes rate-limit, so the reads are batched and paced

Measured against `mainnet.base.org`: an eleven-call batch is rejected outright
(`maximum 10 calls in 1 batch`), and roughly five calls a second get through
however they are packaged — the rest come back as `over rate limit` *inside a
200 response*, which is why `IsTransient` treats that string as the node being
busy rather than as a verdict on the address.

So `Caller` (`internal/source/ethcall.go`, shared with `gen-venues`) batches via
`eth_call` arrays chunked to `MaxBatch`, paces per *call* rather than per request,
memoises every answer for the life of one cycle, and retries only transient
failures. A whole chain is ~90 calls per cycle. **On a shared IP the public Base
endpoint cannot serve that; point `BASE_RPC_URL_8453` at a private node** (or at
least a less contended public one) and the cycle drops from a minute to seconds.

## Protocols

| Protocol | Base subgraph ID | Rate field | Convention | Conversion |
|---|---|---|---|---|
| Aave V3 | `GQFbb95cE6d8mV989mL5figjaGaKCQB3xqYrr1bRyXqF` | `Reserve.liquidityRate` | ray (1e27) **APR** | `RayToAPR` → `APRToAPY` |
| Compound V3 (Comet) | `2hcXhs36pTBDVUmk5K2Zkr6N4UYGwaHuco2a6jyTsijo` | `MarketAccounting.netSupplyApr` | decimal fraction **APR** | `APRToAPY` |
| Moonwell | `33ex1ExmYQtwGVwri1AP3oMFPGSce6YbocBP7fWbsBrg` | `Market.rates[side=LENDER].rate` | **APY percent** already | none |
| Morpho Blue (MetaMorpho) | ours, `MORPHO_SUBGRAPH_ID` (not deployed yet) | `Vault.snapshots[].sharePriceScaled` | **no rate field at all** | `SharePriceGrowthToAPY` over two snapshots |

All conversion math lives in `internal/source/apy.go`, one documented function per
convention, each unit-tested against hand-computed values.

### Morpho Blue: APY derived from share price

Morpho publishes no rate field. A MetaMorpho vault's yield exists only as
appreciation of `totalAssets / totalSupply`, so `internal/source/morpho.go` derives it
from two hourly snapshots of `sharePriceScaled` (`totalAssets * 1e36 / totalSupply`,
a `BigInt`; the `1e36` cancels in the ratio, and the division is done in `math/big`
because a 1e36-scaled integer does not survive a float64 round trip):

    APY% = ((sharePriceScaled_new / sharePriceScaled_old)^(1y / elapsed) - 1) * 100

`elapsed` comes from the two snapshot `timestamp`s actually returned — buckets are
written only in hours where the vault was touched, so they are irregularly spaced.
The widest window inside `MORPHO_MAX_WINDOW` wins; 24h drift is far less noisy than 6h.

A vault is dropped, not published with a doubtful rate, when: fewer than 2 usable
snapshots; window shorter than `MORPHO_MIN_WINDOW` (6h); share price fell (logged at
warn); or `totalSupply == 0`. The subgraph carries no USD price at all, so TVL is
`totalAssets / 10^assetDecimals` multiplied by the Chainlink price (see below).
Non-pegged vaults — mwETH, the cbBTC vaults — are now published rather than dropped;
a vault whose asset has no usable feed is still dropped, never valued at $1.

**`APYReward` is structurally 0 for Morpho.** MORPHO emissions are paid by an off-vault
Universal Rewards Distributor and do not appear in this subgraph; the adapter reports 0
rather than fabricating a number the router would chase.

The official `morpho-org/morpho-blue-subgraph` was archived in March 2025, which is why
`subgraph/morpho-blue-base/` exists.

### `hold`: intrinsic yield, no protocol at all

Every other protocol here is a lending market — you supply, borrowers pay. Some
assets earn without any protocol interaction: a liquid staking token's exchange
rate against its underlying just rises. Reporting that as zero is a mispricing,
not a cosmetic gap. Aave pays ~0.1% on wstETH because nobody borrows it, so a
real 3.1% position reads as 0.1% and the keeper can "improve" a user out of it.

`internal/source/hold.go` maps the sweem subgraph's `RateFeed` entities into
venues with `project = hold`, pool key = the token itself, APY = the intrinsic
rate and TVL = the token's whole supply on Base. Holding the token IS the
position, so there is no deposit call.

The rate is re-derived here from two hourly snapshots by the same
`AnnualizeGrowth` path Morpho share prices take (`internal/source/growth.go`) —
same widest-window-inside-max rule, same minimum window, same refusal on
negative growth. Windows default wider (`HOLD_MIN_WINDOW` 24h) because the
underlying Chainlink exchange-rate feeds run a 24h heartbeat and a six-hour
window can legitimately contain zero rounds.

On Base **none of these tokens can report their own rate** — every one is a
bridged representation, and `stEthPerToken()`, `exchangeRate()`, `getRate()`,
`getEETHByWeETH()` and `convertToAssets()` all revert. The subgraph reads each
rate from the oracle that publishes it on this chain; see
`subgraph/sweem/src/rate-feeds.ts` for the verified table, including the tokens
deliberately left out.

The same rates are **also read directly**, in `internal/source/rpc.go`, and that
path is preferred because it needs no history at all: a Chainlink feed keeps its
past rounds on chain, so `getRoundData(latest - k)` samples the same series the
subgraph would have indexed, and the rate is available on the first cycle instead
of a day later. Sky's oracle is simpler still — `getSUSDSData()` returns `ssr`, a
ray per-second growth factor, so sUSDS needs no sampling (3.60% from `ssr` against
3.54% measured from `chi` drift; the gap is `chi` lagging `rho`, not a
disagreement). Each provider is identified by `description()` before its answer is
used, because a **market price** also drifts and annualizing a market dip would
invent yield — which is exactly why ezETH is excluded on Base. Both paths share
`AnnualizeGrowth` and the same window env vars, so they cannot disagree about
which samples are trustworthy.

`StackIntrinsic` then folds each asset's intrinsic rate into every lending venue
in that asset, after the per-adapter fan-out. `apy` is the total and is the only
field to rank on; `apy_base` (borrower-paid), `apy_reward` (emissions) and
`apy_intrinsic` (protocol issuance) stay separate because they differ in how
durable they are. A rate that could not be measured is **dropped**, never
published as 0%.

### Known gaps

- **Euler V2 Base** (`B48TmxW7Bu56sV2C4YL6TTdTxG9MQYPkU6tXqr18Nv4h`) — published but has
  no indexer allocations; the gateway returns `subgraph not found: no allocations`.
- **Spark / Fluid** — no Base deployment found on the decentralized network.
- Aave's subgraph reports `price.priceInEth = 0` for several reserves (GHO, cbETH,
  wstETH, EURC, weETH). Those are now valued from Chainlink instead of dropped.
- **LBTC and syrupUSDC have no verified aggregator on Base**, so they remain
  unpriceable and their venues are dropped. They show up in `/sources` under
  `unpriceable` rather than silently vanishing.
- **wrsETH and sUSDS have a readable intrinsic rate but no verified USD path on
  Base**, so their `hold` venues are measured and then dropped for want of a
  TVL. They appear in `/sources` under `unpriceable`. Adding a `WRSETH`/`SUSDS`
  entry to `internal/shared/prices` is all that is missing.
- **ezETH's rate is not readable on Base.** The only ETH-denominated ezETH feed
  there, `0x960BDD1d...`, is a market price, not an exchange rate — sampled over
  30 days it fell. No intrinsic APY is emitted for it.
- **syrupUSDC's rate lives on Ethereum mainnet.** The Base token is a bridged
  xERC20 with no rate function and Maple deploys no oracle on Base.

## Asset spelling: the token's own, everywhere it crosses the wire

The executor compares symbols with `!=`. Four strings have to be byte-identical
or a venue is refused on entry *and* on exit:

```
swaps.json "to"  ==  venue.asset  ==  RouteRequest.asset  ==  venues.json symbol
```

`venue.asset` becomes a basket weight in the client, the basket weight becomes
`RouteRequest.asset` in the wallet, and `venues.json symbol` is the on-chain
`symbol()`. So the canonical spelling is **the one the token uses itself** —
`wstETH`, `cbBTC`, `USDbC`, `tBTC` — and `ResolveAsset` returns exactly that.
Every spelling in `assets.go` was read off Base with `symbol()`.

UPPER CASE survives as a **lookup key and nothing else**: the Chainlink feed
tables, `apy:<chain>:<ASSET>`, `isStable` and `StackIntrinsic`'s asset map all
upper-case whatever they are handed. Nothing may compare an asset
case-sensitively against an upper-case literal.

This broke twice in two different places — first `venues.json` against
`swaps.json`, then `venues.json` against the basket asset — so
`TestEmittedSymbolMatchesSwapAllowlist` pins the whole chain: it reads
`executor/swaps.json`, and for every token there asserts that `ResolveAsset` of
that token's *address* returns that exact spelling, and that the generator emits
it unchanged.

## Prices — Chainlink on Base, or the venue is dropped

USD conversion is one mechanism for all four adapters: `internal/source/pricing.go`
wraps the shared `internal/shared/prices` Chainlink client (the same one the wallet
uses) in a per-fetch `Pricer`. Adapters call `Pricer.USD(asset)`; there is no
per-adapter pricing and no `isStable` $1 assumption — even USDC's dollar is *read*
from the USDC/USD aggregator.

**A missing feed, a stale round, a non-positive answer or an RPC failure all mean the
venue is dropped, with a `WARN` line naming the asset and the reason.** Never a
default, never $1, never the price of a similar asset: a wrong TVL misroutes money
exactly as badly as a wrong APY, because TVL gates the liquidity floor.

Who uses it:

| Adapter | USD source |
|---|---|
| Moonwell / Compound V3 | subgraph already reports USD TVL; no price join needed |
| Aave V3 | `price.priceInEth` when non-zero (the venue's own oracle), Chainlink when it is 0 |
| Morpho Blue | Chainlink always — the subgraph has no USD field |
| `RPCSource` (all protocols) | Chainlink always — a chain read carries no USD anything |

An asset that is measured correctly and then cannot be valued (sUSDS and wrsETH
have no USD aggregator on Base) is reported under `unpriceable` in `/sources`
rather than silently vanishing.

Every aggregator address in `prices.Feeds` was verified by calling `description()` on
Base mainnet, and each carries a heartbeat **measured onchain** with `getRoundData`.
Staleness is `heartbeat + PRICE_MAX_AGE`, per feed: USDC/USD and DAI/USD publish on a
24h heartbeat while ETH/USD publishes in minutes, so one global bound would mark a
healthy USDC feed stale and make every USDC venue unroutable.

`wstETH`, `weETH`, `rETH` and `ezETH` have **no direct USD aggregator on Base**, only a
ratio against ETH (`"WSTETH / ETH"`, `"weETH / ETH"`, ...). `prices.RatioFeeds`
composes `ratio x ETH/USD`, and both legs are staleness-checked against their own
heartbeat — a fresh ETH round does not excuse a stale ratio. Using the plain ETH price
for a wrapped staking token would be silently wrong by the whole accrued yield.

`GET /sources` reports what could not be priced:

```json
{"protocol":"aave-v3","ok":true,"venues":8,
 "unpriceable":[{"asset":"LBTC","reason":"prices: no price feed for asset"}]}
```

**Both chains are served at once**: `base` (8453) and `base-sepolia` (84532). Each
gets its own GraphSource, its own price client and its own Redis index
(`venues:index:<label>`, `apy:<label>:<ASSET>`), so one chain's subgraph outage
cannot wipe the other's venues. Venue ids are `<label>:<project>:<pool>`, and the
`chain` query parameter on `/venues`, `/venues/best`, `/assets` and `/sources`
selects one; an unknown label is a 400, never an empty list.

Each chain's feed table comes from its own chain id. On Base Sepolia only USDC and
ETH/WETH have feeds, so every other asset is dropped with a logged reason rather
than priced at a guess.

Point `BASE_RPC_URL_8453` / `BASE_RPC_URL_84532` at real nodes. The public endpoints
rate-limit a cycle's `eth_call` burst, and a rate-limited feed is an unavailable
price — so venues get dropped (correctly, and loudly) rather than mispriced.

## Chain-scoped product rules

What counts as a usable venue is keyed on chain id (`source.FilterFor`), the way
the price tables are, and the publisher and the venue generator share it — a
venue one of them keeps and the other drops is unroutable.

| rule | `base` (8453) | `base-sepolia` (84532) |
|---|---|---|
| zero APY | dropped: nobody is borrowing, or something is broken | **kept**: a testnet market pays 0% because nobody borrows on a testnet, and hiding it hides a venue that works |
| TVL floor | `MIN_TVL_USD`, default 5000 | none, unless `MIN_TVL_USD_84532` is set |
| APY cap | `MAX_APY`, default 100 | 1000 — Sepolia's Aave WETH legitimately prints ~102% |

The bare `MIN_TVL_USD` / `MAX_APY` deliberately do **not** reach the testnet: a
production-sized floor there deletes the entire venue list.

## gen-venues — the executor's allowlist

`cmd/gen-venues` writes `executor/venues.json` from the same indexed data this
service serves, plus on-chain verification. It exists because the allowlist was
hand-written, which capped the product: every venue missing from it is an asset
nobody can put in a basket.

```
go run ./services/market-data/cmd/gen-venues -out executor/venues.json
go run ./services/market-data/cmd/gen-venues -dry-run -min-tvl 1000000
```

Emitted only if all hold: encodable (maps to a `VenueKind`), priceable (verified
Chainlink feed for the underlying on that chain), liquid (clears the TVL floor),
verified on chain (`symbol()`/`decimals()` plus a protocol identity check), and
the id equals `venue.MakeID(chain, project, pool)` — asserted, because a
mismatch silently breaks routing. Everything else is printed with its reason.

Candidates come from **both sources, already reconciled** — the same
`source.Reconcile` the publisher uses, over the same `GraphSource` +
`RPCSource` pair. Drawing them from the subgraph alone omitted every `hold`
venue the moment intrinsic rates moved to the direct reader: measured correctly,
and unroutable. A venue the publisher serves but the generator omits cannot be
routed to; a venue the generator emits but the publisher never serves is dead
weight in the allowlist. One composition function means the two cannot disagree
about what a venue is.

`hold` venues are emitted too: they have no protocol call, so the target IS the
asset, which is the one case the executor's "target must not be the asset" rule
exempts.

It is a build-time tool: the executor reads the committed file and never fetches
an allowlist at runtime, so the addresses it can call stay outside the request
path. Re-run it whenever a subgraph starts indexing more, and read the diff.

## Env vars

| Var | Default | Meaning |
|---|---|---|
| `GRAPH_API_KEY` | — | **required**, both binaries read it; publisher exits if unset |
| `GRAPH_GATEWAY_URL` | `https://gateway.thegraph.com/api/subgraphs/id` | gateway base |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection |
| `MARKET_DATA_ADDR` | `:8081` | API listen address |
| `POLL_INTERVAL` | `5m` | publisher cycle; Redis TTL is 3× this |
| `CHAINS` | `base,base-sepolia` | chains polled each cycle, one source per chain; an unknown label is fatal at boot |
| `MIN_TVL_USD` | `5000` | minimum computed USD TVL. **Mainnet only** — see the chain rules below |
| `MAX_APY` | `100` | sanity cap, rejects reward-spike artifacts. Mainnet only |
| `MIN_TVL_USD_<chainid>` / `MAX_APY_<chainid>` | — | per-chain override; the only way to move the testnet thresholds |
| `AAVE_V3_SUBGRAPH_ID_<chainid>` / `COMPOUND_V3_SUBGRAPH_ID_<chainid>` / `MOONWELL_SUBGRAPH_ID_<chainid>` / `MORPHO_SUBGRAPH_ID_<chainid>` | — | per protocol **per chain**. Unset means the adapter reports unconfigured in `/sources` for that chain and contributes no venues — it never falls back to another chain's deployment. A bare id is appended to `GRAPH_GATEWAY_URL`; a value starting with `http` (Studio: account path + version) is used verbatim. |
| `MORPHO_MIN_WINDOW` | `6h` | shortest snapshot gap that may be annualized |
| `MORPHO_MAX_WINDOW` | `168h` | widest snapshot gap considered |
| `HOLD_SUBGRAPH_ID_<chainid>` | — | same rules as the other subgraph ids; points at the sweem deployment that carries `RateFeed` |
| `HOLD_MIN_WINDOW` | `24h` | shortest exchange-rate gap that may be annualized (feeds have a 24h heartbeat) |
| `HOLD_MAX_WINDOW` | `336h` | widest exchange-rate gap considered. Shared by both sources, so they cannot disagree about which samples are trustworthy |
| `RATE_DISAGREEMENT_TOLERANCE` | `0.25` | percentage points of APY the two sources may differ before the gap is logged |
| `SOURCE_FETCH_TIMEOUT` | `2m` | per-source budget for one cycle. The direct-RPC source paces itself against a rate-limited node, so it needs more than a subgraph query does |
| `BASE_RPC_URL_8453` / `BASE_RPC_URL_84532` | public Base endpoints | one node per chain for Chainlink prices. No shared fallback: a price read off the wrong network is a wrong number. |
| `PRICE_MAX_AGE` | `1h` | grace allowed *on top of* each feed's own measured heartbeat |
| `PRICE_CACHE_TTL` | `1m` | how long a fetched price is reused |
| `ENV_FILE` | `.env` | dotenv file loaded at startup (real env always wins) |
| `LOG_LEVEL` | `0` (info) | slog level, `-4` for debug |

Config comes from the repo-root `.env` (gitignored). Keep `.env.example` in sync.

## Running

Redis must be running locally.

```bash
go run ./services/market-data/cmd/publisher   # scanner, blocks and ticks
go run ./services/market-data/cmd/api         # http api on :8081
```

## Redis key schema

| Key | Type | Contents |
|---|---|---|
| `venue:{chain}:{project}:{poolID}` | HASH | one venue: `id chain project symbol pool_id asset tvl_usd apy apy_base apy_reward apy_intrinsic stablecoin updated_at` |
| `apy:{chain}:{ASSET}` | ZSET | score = APY, member = venue ID — the "best venue for asset" index |
| `venues:index` | SET | every live venue ID |
| `venues:sources` | STRING | JSON array of per-protocol fetch status, powers `/sources` |
| `venues:updated` | PUBSUB | `{"count":N,"at":"<rfc3339>"}` after each successful cycle |

Everything carries a TTL of 3× `POLL_INTERVAL`, so a dead publisher expires its own
data instead of serving stale yields forever. Each cycle rewrites the ZSETs whole.

## API

Success bodies are `{"data": ...}`, errors are `{"error":{"code","message"}}`.
`/health` is the one exception: it returns its fields flat, for probes.

```bash
curl 'localhost:8081/health'
# {"redis":"up","status":"ok","venues":10}

curl 'localhost:8081/venues?chain=Base&asset=USDC&limit=2'
# {"data":{"count":2,"venues":[{...}]}}

curl 'localhost:8081/venues/best?asset=USDC&chain=Base&min_tvl=5000000'
# {"data":{"id":"Base:moonwell:0xedc817...","project":"moonwell","asset":"USDC","apy":14.51,...}}
# 404 {"error":{"code":"no_venue","message":"no venue matches asset=DOGE chain= min_tvl="}}

curl 'localhost:8081/assets?chain=Base'
# {"data":{"count":5,"assets":[{"asset":"USDC","venues":3,"best_apy":14.51,"best_venue":"...","total_tvl_usd":183119324}]}}

curl 'localhost:8081/sources'
# {"data":{"count":3,"sources":[{"protocol":"aave-v3","ok":true,"venues":8,"last_success":"...",
#   "unpriceable":[{"asset":"LBTC","reason":"prices: no price feed for asset"}]}]}}
```

## Adding a protocol

One new file in `internal/source/`: implement `ProtocolAdapter` (protocol name, subgraph
id, GraphQL document, `Map(p *Pricer, raw)`), then `Register` it from that file's `init()`. Nothing else
changes. An adapter with no configured subgraph id stays in the list and reports as
failed in `/sources` (rather than vanishing), and one adapter failing never fails a cycle.

## Tests

`go test ./services/market-data/...` — no network. Mapper fixtures under
`internal/source/testdata/` were captured from real gateway queries, except
`morpho.json`, hand-built from `subgraph/morpho-blue-base/schema.graphql` because that
subgraph is not deployed yet.
