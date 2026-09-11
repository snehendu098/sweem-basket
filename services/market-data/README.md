# market-data

Answers one question for the rest of the protocol: **where is the best yield for token X right now?**

Two binaries, one Redis, one data source (The Graph):

- `cmd/publisher` — polls protocol subgraphs on an interval, normalizes every protocol's
  rate convention into one `Venue` schema, rewrites Redis.
- `cmd/api` — read-only HTTP API over that Redis state. The wallet service calls
  `/venues/best` to decide where to route a deposit.

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

### Known gaps

- **Euler V2 Base** (`B48TmxW7Bu56sV2C4YL6TTdTxG9MQYPkU6tXqr18Nv4h`) — published but has
  no indexer allocations; the gateway returns `subgraph not found: no allocations`.
- **Spark / Fluid** — no Base deployment found on the decentralized network.
- Aave's subgraph reports `price.priceInEth = 0` for several reserves (GHO, cbETH,
  wstETH, EURC, weETH). Those are now valued from Chainlink instead of dropped.
- **LBTC and syrupUSDC have no verified aggregator on Base**, so they remain
  unpriceable and their venues are dropped. They show up in `/sources` under
  `unpriceable` rather than silently vanishing.

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

Deployment target is **Base Sepolia (chain 84532)**; `CHAIN_ID` selects the Chainlink
feed table (`84532` default, `8453` for mainnet). On Base Sepolia only USDC and
ETH/WETH have feeds, so every other asset is dropped with a logged reason rather
than priced at a guess.

Point `BASE_RPC_URL` at a real node. The public `sepolia.base.org` rate-limits a
cycle's `eth_call` burst, and a rate-limited feed is an unavailable price — so venues
get dropped (correctly, and loudly) rather than mispriced.

## Env vars

| Var | Default | Meaning |
|---|---|---|
| `GRAPH_API_KEY` | — | **required**, both binaries read it; publisher exits if unset |
| `GRAPH_GATEWAY_URL` | `https://gateway.thegraph.com/api/subgraphs/id` | gateway base |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection |
| `MARKET_DATA_ADDR` | `:8081` | API listen address |
| `POLL_INTERVAL` | `5m` | publisher cycle; Redis TTL is 3× this |
| `CHAINS` | `Base` | comma-separated chain allowlist; first entry drives the adapter set |
| `MIN_TVL_USD` | `50000` | minimum computed USD TVL for a venue (testnet-scaled) |
| `MAX_APY` | `100` | sanity cap, rejects reward-spike artifacts |
| `AAVE_V3_SUBGRAPH_ID` / `COMPOUND_V3_SUBGRAPH_ID` / `MOONWELL_SUBGRAPH_ID` | live IDs above | override a deployment |
| `MORPHO_SUBGRAPH_ID` | — | our Morpho Blue deployment; unset means the adapter reports as unconfigured in `/sources` and contributes no venues |
| `MORPHO_MIN_WINDOW` | `6h` | shortest snapshot gap that may be annualized |
| `MORPHO_MAX_WINDOW` | `168h` | widest snapshot gap considered |
| `BASE_RPC_URL` | `https://sepolia.base.org` | Base node for Chainlink prices (same var the executor, wallet and keeper use). The public endpoint rate-limits and caps `eth_getLogs` at 10k blocks. |
| `CHAIN_ID` | `84532` | selects the Chainlink feed table (`8453` = Base mainnet) |
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
| `venue:{chain}:{project}:{poolID}` | HASH | one venue: `id chain project symbol pool_id asset tvl_usd apy apy_base apy_reward stablecoin updated_at` |
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
