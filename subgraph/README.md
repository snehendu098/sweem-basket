# subgraph/

One subgraph, three protocols, one schema.

`sweem/` indexes **Aave V3**, **Compound III (Comet)** and **MetaMorpho
(Morpho Blue)** from a single manifest and normalizes all three into one
`Venue` entity whose `supplyApy` is already a comparable percentage. The same
project deploys to **Base Sepolia** and **Base mainnet** — `networks.json`
carries both, and `graph build --network <name>` picks one.

It replaces the three separate projects that used to live here
(`aave-v3-base-sepolia/`, `compound-v3-base-sepolia/`, `morpho-blue-base/`).
The per-protocol indexing logic is unchanged and still verified against live
data; only the packaging and the normalization layer are new.

## Why one subgraph

Two reasons, and they point the same way.

**Slots.** Subgraph Studio allows 3 subgraphs. One per protocol per chain does
not survive contact with a second chain — Sepolia alone used all three, and one
had to be deleted to make room. One subgraph per *chain* leaves headroom.

**The deliverable.** Normalizing three rate conventions into one comparable
number is the interesting part of this system. Doing it in Go on top of three
subgraphs makes it a claim about our service. Doing it inside the subgraph
makes the normalized schema the artifact: anyone can query `venues` and get
Aave, Compound and Morpho side by side, already compounded the same way.

## The normalized entity

```graphql
type Venue @entity {
  id: ID!                  # "Base:aave-v3:0x036cbd53842c...", the executor allowlist key
  chain: String!           # "Base"
  protocol: String!        # aave-v3 | compound-v3 | morpho-blue
  pool: Bytes!             # what the protocol addresses a supply position by
  symbol: String!
  asset: Bytes!
  assetSymbol: String!
  assetDecimals: Int!
  supplyApy: BigDecimal!   # PERCENT, per-second compounded, normalized
  totalSupply: BigInt!     # raw underlying units, scale by assetDecimals
  utilization: BigDecimal  # 0..1, null for MetaMorpho
  isActive: Boolean!
  lastUpdateBlock: BigInt!
  lastUpdateTimestamp: BigInt!
}
```

`id` is `<chain>:<protocol>:<pool>` with `pool` lowercase hex, byte-for-byte the
key `executor/venues.json` uses. `pool` is whatever the protocol addresses a
supply position by, which is different in all three cases:

| protocol | `pool` is | why |
|---|---|---|
| `aave-v3` | the underlying reserve asset | you supply an asset to the shared Pool |
| `compound-v3` | the Comet proxy | Comet is one contract per base asset |
| `morpho-blue` | the MetaMorpho vault | the vault is the ERC-4626 you deposit into |

`supplyApy` of `0` means *no trustworthy rate yet* — notably a Morpho vault with
under six hours of share-price history. It never means "0% yield, route here".

The per-protocol detail entities (`Reserve`, `Market`/`MarketAccounting`,
`Vault`, and the three hourly snapshot series) are all still present and
unchanged, so anything already querying them keeps working.

## How each protocol's rate becomes `supplyApy`

All three end in the same compounding step, done as `Expm1(n · Log1p(r))`
rather than `Pow(1+r, n)`: with a per-second `r` of ~1e-9, `1 + r` discards most
of `r`'s significant digits in f64. This mirrors
`services/market-data/internal/source/apy.go` exactly — `bun run check` in
`sweem/` pins the numbers.

**Aave V3** — `liquidityRate` from `ReserveDataUpdated` is a ray (1e27) annual
*simple* rate.

```
apr = liquidityRate / 1e27
apy = (expm1(31_536_000 · log1p(apr / 31_536_000))) · 100
```

Verified on Base Sepolia WETH: `704152479263146270890280563 / 1e27` = 70.415%
APR → **102.213% APY**.

**Compound III** — Comet emits no rate event at all, so the rate is read at
every state change from `getSupplyRate(getUtilization())`. That value is a
**per-second** growth rate scaled 1e18, *not* per block — a per-block conversion
would be roughly 2x wrong on Base's 2s blocks.

```
apr = supplyRatePerSecond / 1e18 · 31_536_000
apy = (expm1(31_536_000 · log1p(apr / 31_536_000))) · 100
```

Utilization only moves when one of the indexed events fires, and the rate is a
pure function of utilization, so the event-driven refresh is exact rather than a
sample.

**MetaMorpho** — no rate field exists anywhere. Yield appears only as ERC-4626
share-price growth, so it needs a time series:

```
sharePriceScaled = totalAssets · 1e36 / totalSupply
growth           = sharePriceScaled_now / sharePriceScaled_anchor - 1
periods          = 31_536_000 / elapsedSeconds
apy              = (expm1(periods · log1p(growth))) · 100
```

1e36 and not 1e18 because MetaMorpho shares carry a virtual decimals offset —
`DECIMALS_OFFSET()` is 12 on a USDC vault — so the raw ratio is ~1e-12 and 1e18
would leave about six significant digits, which rounds an hour of yield away.

The vault holds an anchor sample (`anchorSharePriceScaled`,
`anchorTimestamp`). No APY is published until the window is at least **6 hours**
wide, and the anchor is dragged forward once it passes **24 hours** so the
published number is recent yield rather than a lifetime average. A falling share
price publishes no rate at all. `VaultHourlySnapshot` is still written on every
touch, so a consumer that wants a different window can compute it itself.

## Data sources

Every address below was located by binary-searching `eth_getCode` (block `N-1`
returns `0x`, block `N` returns bytecode) and reproduced on a second independent
archive RPC. Mainnet addresses were additionally confirmed by identity, not just
by having code: `Pool.ADDRESSES_PROVIDER()` and `provider.getPool()` /
`getPoolConfigurator()` round-trip, and each Comet answers `symbol()` with the
market it claims to be.

### Base Sepolia (84532) — `sepolia.base.org`, `base-sepolia.drpc.org`

| Data source | Address | Start block |
|---|---|---|
| `PoolConfigurator` | `0x347Ae6820F48e9Dd563235742d89FAef6ffCaA72` | 6719154 |
| `Pool` | `0x07eA79F68B2B3df564D0A34F8e19D9B1e339814b` | 6719154 |
| `Comet` (cUSDCv3) | `0x571621Ce60Cebb0c1D442B5afb38B1663C6Bf017` | 7646646 |
| `CometWeth` (cWETHv3) | `0x61490650AbaA31393464C3f34E8B29cd1C44118E` | 7907557 |
| `MetaMorphoFactory` (v1.0) | `0xA9c3D3a366466Fa809d1Ae982Fb2c46E5fC41101` | 9315415 |
| `MetaMorphoV1_1Factory` | *not deployed* — see below | — |

### Base mainnet (8453) — `mainnet.base.org`, `base.drpc.org`

| Data source | Address | Identity check |
|---|---|---|
| `PoolConfigurator` | `0x5731a04B1E775f0fdd454Bf70f3335886e9A96be` | `provider.getPoolConfigurator()` |
| `Pool` | `0xA238Dd80C259a72e81d7e4664a9801593F98d1c5` | `provider.getPool()`, and its `ADDRESSES_PROVIDER()` points back |
| `Comet` (cUSDCv3) | `0xb125E6687d4313864e53df431d5425969c15Eb2F` | `symbol() == "cUSDCv3"`, base `0x8335…2913` |
| `CometWeth` (cWETHv3) | `0x46e6b214b524310239732D51387075E0e70970bf` | `symbol() == "cWETHv3"`, base `0x4200…0006` |
| `MetaMorphoFactory` (v1.0) | `0xA9c3D3a366466Fa809d1Ae982Fb2c46E5fC41101` | deploy block 13978134, boundary confirmed |
| `MetaMorphoV1_1Factory` | `0xFf62A7c278C62eD665133147129245053Bbf5918` | deploy block 23928808, boundary confirmed |

The Aave `PoolAddressesProvider` is **not** a data source and is not hardcoded:
the mapping learns the Pool from `event.address` (or from `dataSource.address()`
in the start-block handler), then resolves the provider and the oracle through
it and memoizes the pair in an `AaveRegistry` singleton. That is what lets one
mapping serve both networks. For reference it is
`0xd449FeD49d9C443688d6816fE6872F21402e41de` on Sepolia and
`0xe20fCBdBfFC4Dd138cE8b2E6FBb6CB49777ad64D` on mainnet, both confirmed by
round-trip.

### Mainnet start blocks are deliberately recent — do not "fix" them

**Every mainnet data source starts at block 51,120,000, not at its deployment
block.** This is intentional and the numbers above are still the real deploy
blocks, recorded so nobody has to re-derive them.

We never query history. Aave's rate is the current `liquidityRate`, Comet's is
the current `getSupplyRate(getUtilization())`; both are spot reads needing no
window at all. Only two paths *derive* a rate from a series, and the start block
is sized to the wider of them:

| Path | Minimum window to produce any rate | Re-anchor |
|---|---|---|
| Morpho share-price growth | 6h (~10,800 blocks) | 24h |
| `RateFeeds` LST exchange rates | 24h (~43,200 blocks) | 7d |

51,120,000 is ~106,000 blocks back, about 2.45 days of Base at 2s — 2.4x the
widest minimum. Re-anchor intervals are a staleness cap, not a requirement to
produce a first reading, so 7d of history is not needed to start.

Indexing from deployment meant scanning 49M blocks to serve numbers that are
identical either way. Measured at ~300k blocks/hour that is ~6.5 days; ~106k
blocks is minutes.

**Do not set a start block below ~60,000 blocks back.** Under a 24h window
`RateFeeds` has no anchor pair and publishes no rate, which is indistinguishable
from "this asset earns nothing" — the exact failure this codebase keeps
designing against. A fast sync that indexes nothing is worse than a slow one.

Base Sepolia start blocks stay at their deploy blocks; that chain is small
enough that it does not matter, and it keeps the full history there.

A late start block is only safe because two **discovery** dependencies were
removed first. Both fired long before block 50.7M, so leaving them in place
would have produced a fast sync that indexed nothing — worse than a slow one:

**Aave — `ReserveInitialized`** carried the aToken and variableDebtToken
addresses, and without those supplies a reserve reports zero TVL. Two
replacements, both `try_`:

* `handlePoolInit`, a `once` block handler on the `Pool` data source, reads
  `getReservesList()` at the start block and seeds every reserve — so the venue
  set is deterministic from block one rather than "whatever happened to trade".
* `ensureTokens()` fills the token addresses from `Pool.getReserveAToken()` /
  `getReserveVariableDebtToken()` on every refresh until they are known.

Those two getters exist from Aave 3.2 onward. They are present on the Base
mainnet Pool and **revert on the older Base Sepolia Pool** — verified by direct
`eth_call` on both. That is exactly why they are `try_`: on Sepolia the
addresses keep arriving on `ReserveInitialized` the way they always did, and
nothing changes there.

Rates are not seeded at the start block — `liquidityRate` only exists on the
event. Any reserve with activity emits `ReserveDataUpdated` within minutes, well
before the subgraph reaches head.

**Morpho — `CreateMetaMorpho`** is how vaults are discovered, and it also spawns
the template that indexes them. On Base those events fired between blocks ~13.9M
and ~23.9M, and each template would then replay that vault's entire life; for a
$420M vault that is millions of events at two eth_calls apiece. So the
pre-existing vaults are **adopted by address** in `handleFactoryInit`, a `once`
block handler that builds each `Vault` from on-chain reads and calls
`MetaMorphoTemplate.create()` — which starts indexing from the start block, not
from creation. The factories remain in the manifest, so vaults created from
50.7M onward are still discovered normally.

The nine adopted vaults, each verified on-chain before being listed —
`MORPHO()` returns Morpho Blue on Base (`0xBBBBBbbBBb9cC5e90e3b3Af64bdAF62C37EEFFCb`),
`asset()` and `symbol()` match, `totalAssets()` non-zero:

| Vault | Symbol | Asset | TVL at verification |
|---|---|---|---|
| `0xeE8F4eC5672F09119b96Ab6fB59C27E1b7e44b61` | gtUSDCp | USDC | $420.2M |
| `0x7BfA7C4f149E7415b73bdeDfe609237e29CBF34A` | sparkUSDC | USDC | $277.9M |
| `0xbeeF010f9cb27031ad51e3333f9aF9C6B1228183` | steakUSDC | USDC | $131.3M |
| `0xBeEf2d50B428675a1921bC6bBF4bfb9D8cF1461A` | grove-bbqUSDC | USDC | $67.1M |
| `0x2C6D169782bF18Cc634D076Fe639092227B82fdA` | frUSDC | USDC | $26.8M |
| `0xBEEFE94c8aD530842bfE7d8B397938fFc1cb83b2` | steakUSDC (Prime) | USDC | $20.5M |
| `0x1401d1271C47648AC70cBcdfA3776D4A87CE006B` | pUSDC | USDC | $11.9M |
| `0xc1256Ae5FF1cf2719D4937adb3bbCCab2E00A2Ca` | mwUSDC | USDC | $4.6M |
| `0xa0E430870c4604CcfC7B38Ca7845B1FF653D0ff1` | mwETH | WETH | $4.1M |

The list lives in `knownVaults()` in `src/morpho-factory.ts`. Adopted vaults
record `version: 0` (factory not observed) and a `createdAtBlock` of the start
block rather than their true creation block — the one place the late start is
visible in the data. `mwETH` has `DECIMALS_OFFSET() == 0` while every USDC vault
has `12`, which is why the 1e36 share-price scale is not optional.

`RateFeeds` was moved from 47,340,000 to 51,120,000 along with everything else.
It is not one of ours, but as the earliest source it set the floor on where the
whole sync began, so leaving it behind would have cost ~3.9M blocks — roughly 13
hours — and bought nothing: its own `MIN_APY_WINDOW` is 24h, which 106,000
blocks covers more than twice over.

### Per-network scoping, and where the manifest cannot help

A manifest data source cannot be omitted on one network and present on another —
`networks.json` patches `address` and `startBlock` per network, but the list of
data sources itself is fixed. Only one entry actually differs in existence:
MetaMorpho v1.1, which is on mainnet but not on Base Sepolia.

Rather than split the project into two manifests for one contract, the Sepolia
entry for `MetaMorphoV1_1Factory` points at the zero address. It matches
nothing, indexes nothing, and costs nothing. It is the one deliberate piece of
dead configuration here and it is in `networks.json`, not in code.

If a future network needs a genuinely different *set* of protocols, that is the
point to split — a second manifest sharing `schema.graphql`, `src/` and `abis/`,
built with `graph build subgraph.<net>.yaml --network <net>`. It is not needed
today and would buy nothing.

**Mainnet sync time.** With every source at 50,700,000 and `RateFeeds` at
47,340,000, the deployment walks ~3.9M blocks, of which only the last ~525k have
the full filter active. Expect minutes, not days. See the section above for why
those start blocks must not be moved back.

## Build

```sh
cd subgraph/sweem
bun install
bunx graph codegen                        # once; the manifest is the superset
bun run check                             # rate conversions agree with apy.go

bunx graph build --network base-sepolia
bunx graph build --network base
```

Both builds pass. `graph build --network` rewrites `subgraph.yaml` in place with
that network's addresses, so whichever you ran last is what is checked in —
harmless, since `deploy` re-applies `--network` anyway.

## Deploy

Do **not** deploy over the live Sepolia subgraphs until this one is verified
returning venues; see the slot plan below.

```sh
# once per machine, key from https://thegraph.com/studio/
bunx graph auth <DEPLOY_KEY>

cd subgraph/sweem

# Base Sepolia — all three protocols
bunx graph codegen && bunx graph build --network base-sepolia
bunx graph deploy sweem-base-sepolia --network base-sepolia

# Base mainnet — all three protocols
bunx graph codegen && bunx graph build --network base
bunx graph deploy sweem-base --network base
```

Studio prints a version label prompt and then the deployment id / query URL.

## Wiring the deployed id into the Go side

`services/market-data` resolves one subgraph id per protocol adapter. All three
now point at the **same** deployment, because it is the same subgraph:

| Env var | Read by | Set to |
|---|---|---|
| `AAVE_V3_SUBGRAPH_ID` | `internal/source/aave_v3.go` | the `sweem` deployment id for this chain |
| `COMPOUND_V3_SUBGRAPH_ID` | `internal/source/compound_v3.go` | same id |
| `MORPHO_SUBGRAPH_ID` | `internal/source/morpho.go` | same id |

No adapter code has to change: `Reserve`, `Market` and `Vault` are all in the
unified schema under the names those adapters already query. Pointing the three
variables at one id is the whole migration.

Once `SubgraphID()` values starting with `http` are used verbatim (in flight
separately), these can equally hold a full Studio query URL.

## Mainnet fallback while our deployment syncs

Public, fully-synced Base mainnet subgraphs on the decentralized gateway:

```
aave-v3      GQFbb95cE6d8mV989mL5figjaGaKCQB3xqYrr1bRyXqF
compound-v3  2hcXhs36pTBDVUmk5K2Zkr6N4UYGwaHuco2a6jyTsijo
moonwell     33ex1ExmYQtwGVwri1AP3oMFPGSce6YbocBP7fWbsBrg
```

Point `AAVE_V3_SUBGRAPH_ID` and `COMPOUND_V3_SUBGRAPH_ID` at these if mainnet
has to serve before `sweem-base` is caught up, then cut both over. With recent
start blocks the sync window is short enough that this should rarely be needed.
It is a bridge, not a dependency — there is no maintained public Morpho
subgraph, which is part of why this one exists.

## Slot math

Studio allows **3** subgraphs and one has already been deleted to make room.

```
sweem-base-sepolia    testnet, all three protocols   <- deploy first
sweem-base            mainnet, all three protocols
(spare)
```

`aave-v-3-base-sepolia` and `compound-v-3-base-sepolia` are **currently serving
the running stack**. Do not delete them to free a slot until `sweem-base-sepolia`
is deployed *and* verified returning venues. The spare slot exists precisely so
that cutover does not have to be a leap.

## Scope

Deliberately not indexed, in all three protocols: users, positions, borrow-side
accounting beyond what utilization needs, liquidations, e-mode, and reward
emissions. A yield router needs to know where the yield is and how much is
there. `rewardSupplyApr` is structurally `0` — COMP emissions are not valued
here and MORPHO emissions are paid by an off-vault Universal Rewards
Distributor that is not on-chain in this subgraph. Fabricating a number would
make the router chase yield the venue does not pay.
