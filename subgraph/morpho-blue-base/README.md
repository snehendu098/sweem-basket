# morpho-blue-base

A subgraph for **MetaMorpho vaults on Morpho Blue**. Currently pointed at
**Base Sepolia (chain 84532)**; the Base mainnet (chain 8453) configuration is
kept in `networks.json` and is restorable — see *Networks* below.

Originally written for **Base mainnet** (chain 8453) — the ERC-4626 wrappers
over Morpho Blue that depositors actually interact with (steakUSDC, gtUSDCp,
mwUSDC, bbqUSDC, …).

Morpho Blue is the largest lending protocol on Base by TVL. The official
subgraph (`morpho-org/morpho-blue-subgraph`) was archived in March 2025 and the
Morpho Association no longer maintains it, so there is currently no maintained
subgraph for the biggest lending venue on the chain. This fills that gap.

## What it indexes

| Entity | What it is |
|---|---|
| `Vault` | one MetaMorpho vault: name, symbol, underlying asset, TVL in underlying units, total shares, current share price, curator, owner, fee, cumulative flows |
| `VaultHourlySnapshot` | share price / TVL sampled into hourly buckets — the yield signal |
| `VaultDeposit` / `VaultWithdraw` | every deposit and withdrawal, so TVL flow is queryable |

Underlying Morpho Blue *markets* are **not** indexed. Vaults are what a
depositor and a router touch; markets are a follow-up.

### Why a share-price series at all

Morpho publishes no APY field. A MetaMorpho vault's yield is only visible as
appreciation of `totalAssets / totalSupply`. Without a historical series a
consumer cannot compute a rate at all — which is exactly why this subgraph
exists rather than a simple TVL indexer.

`sharePriceScaled` is `totalAssets * 1e36 / totalSupply` as a `BigInt`. Two of
those, plus the elapsed time between their `timestamp`s, feed directly into
`SharePriceGrowthToAPY(before, after, elapsed)` in
`services/market-data/internal/source/apy.go`. The scale cancels in the ratio,
so no decimals bookkeeping is needed on the consumer side.

`1e36` and not `1e18`: MetaMorpho shares carry a virtual decimals offset (a USDC
vault has 6-decimal assets and 18-decimal shares), so the raw ratio is around
`1e-12`. At `1e18` scale that leaves ~6 significant digits — an hour of yield at
5% APY moves the price by ~6e-6 relative, which would be lost in rounding. At
`1e36` there are ~24 significant digits and an hourly delta is exact.

## Snapshot interval: hourly, event-driven, with a real timestamp

**Hourly buckets, last write in the hour wins, keyed `<vault>-<hourIndex>`.**

The trade-off:

- *Per-event snapshots* give the finest resolution but irregular spacing, and
  one entity per event on a vault touched hundreds of times a day. Storage grows
  with traffic, not with time, and a consumer asking "price 24h ago" has to
  scan.
- *Fixed hourly/daily buckets filled by a block handler* give perfectly regular
  spacing, but a block handler on every Base block (2s) across every vault is
  prohibitively expensive to index and is the main reason vault subgraphs fall
  behind.
- **Hourly buckets driven by vault events** — what this does — give at most 24
  rows per vault per day regardless of traffic, `first: N, orderBy: hourIndex,
  orderDirection: desc` answers "the last N hours" in one query, and indexing
  cost stays proportional to real activity.

The one cost is that a bucket exists only if the vault was touched in that hour,
so the series has gaps for quiet vaults. That is handled rather than ignored:
every snapshot carries `timestamp`, the wall-clock time of the last sample
folded into it, so the consumer computes `elapsed` from the two samples it
actually received instead of assuming the nominal one-hour spacing.
`SharePriceGrowthToAPY` takes an explicit `elapsed` for exactly this reason, so
an irregular gap is correct, not approximated.

In practice the large Base USDC vaults are touched many times an hour — Morpho's
`AccrueInterest` / `UpdateLastTotalAssets` fire on *every* vault interaction, not
just deposits and withdrawals, so the series is dense where it matters and
sparse only where the vault is genuinely idle.

## Vault discovery: factory + template

Vaults are discovered from the **MetaMorpho factory**, not hardcoded — a
hardcoded list goes stale the moment a new vault launches, which for Morpho is
roughly weekly.

On **Base Sepolia only the v1.0 factory exists**, so only it is indexed. It
emits `CreateMetaMorpho`, which spawns a `MetaMorpho` template data source per new vault:

| Network | Factory | Address | Start block |
|---|---|---|---|
| base-sepolia | MetaMorpho v1.0 | `0xA9c3D3a366466Fa809d1Ae982Fb2c46E5fC41101` | **9315415** |
| base | MetaMorpho v1.0 | `0xA9c3D3a366466Fa809d1Ae982Fb2c46E5fC41101` | 13978134 |
| base | MetaMorpho v1.1 | `0xFf62A7c278C62eD665133147129245053Bbf5918` | 23928808 |

**MetaMorpho v1.1 is not deployed on Base Sepolia.** `eth_getCode` on
`0xFf62A7c2…` returns `0x` at head, so that data source is removed from the
manifest rather than left pointing at an empty address.

The v1.0 factory and Morpho Blue itself carry the *same addresses* on Base
Sepolia as on Base mainnet (both are deterministic deployments), so only the
network name and the start blocks change.

Base Sepolia start blocks were found by binary search on `eth_getCode` against
`https://sepolia.base.org` and re-verified on `https://base-sepolia.drpc.org`
(block `N-1` → `0x`, block `N` → bytecode):

| Contract | Address | Deploy block |
|---|---|---|
| Morpho Blue | `0xBBBBBbbBBb9cC5e90e3b3Af64bdAF62C37EEFFCb` | 9304957 |
| MetaMorpho v1.0 factory | `0xA9c3D3a366466Fa809d1Ae982Fb2c46E5fC41101` | 9315415 |

The factory's account nonce on Base Sepolia is **38**, i.e. 38 vaults have been
created there — so this is a populated market, not an empty testnet stub.

Verified on Base mainnet when the subgraph was first written, and unchanged
here because the ABI and event layout are identical:

- `isMetaMorpho(0xbeeF010f9cb27031ad51e3333f9aF9C6B1228183)` (steakUSDC) returns
  `true` on the v1.0 factory and `false` on v1.1 — they keep separate registries.
- The `CreateMetaMorpho` log for steakUSDC was pulled from block 15183452 to
  confirm the event's indexed-parameter layout (`metaMorpho`, `caller`, `asset`
  indexed; `initialOwner`, `initialTimelock`, `name`, `symbol`, `salt` in data).
- Every vault event topic in `abis/MetaMorpho.json` was confirmed present in
  steakUSDC's deployed bytecode.

Morpho Blue itself is at `0xBBBBBbbBBb9cC5e90e3b3Af64bdAF62C37EEFFCb` on both
networks — recorded for the market-level follow-up, not indexed yet.

USDC is `0x036CbD53842c5426634e7929541eC2318f3dCF7e` (6 decimals) on Base
Sepolia and `0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913` on Base mainnet.

## Networks

`networks.json` holds both networks, so neither is lost:

```json
{
  "base-sepolia": { "MetaMorphoFactory": { "address": "0xA9c3…", "startBlock": 9315415 } },
  "base":         { "MetaMorphoFactory": { "address": "0xA9c3…", "startBlock": 13978134 },
                    "MetaMorphoV1_1Factory": { "address": "0xFf62…", "startBlock": 23928808 } }
}
```

`graph build --network <name>` rewrites `subgraph.yaml`'s addresses and start
blocks from that file, so the network is a build-time flag, not an edit.

**Building for Base Sepolia (the default):**

```sh
bunx graph build --network base-sepolia
```

**Going back to Base mainnet:** the address/start-block half is
`--network base`, but `--network` cannot *add* a data source. Re-add the v1.1
factory block to `subgraph.yaml` first (it is not deployed on Sepolia, which is
why it was removed) — the handler `handleCreateMetaMorphoV1_1` is still present
in `src/factory.ts` for exactly this:

```yaml
  - kind: ethereum
    name: MetaMorphoV1_1Factory
    network: base
    source:
      address: "0xFf62A7c278C62eD665133147129245053Bbf5918"
      abi: MetaMorphoFactory
      startBlock: 23928808
    mapping:
      kind: ethereum/events
      apiVersion: 0.0.9
      language: wasm/assemblyscript
      file: ./src/factory.ts
      entities:
        - Vault
      abis:
        - name: MetaMorphoFactory
          file: ./abis/MetaMorphoFactory.json
        - name: MetaMorpho
          file: ./abis/MetaMorpho.json
        - name: ERC20
          file: ./abis/ERC20.json
      eventHandlers:
        - event: CreateMetaMorpho(indexed address,indexed address,address,uint256,indexed address,string,string,bytes32)
          handler: handleCreateMetaMorphoV1_1
```

then `bunx graph build --network base`. Also flip `network:` on the
`MetaMorpho` template to `base`.

## Build

```sh
bun install          # or: npm install
bunx graph codegen
bunx graph build --network base-sepolia
```

Both must pass before deploying.

## Deploy

Not deployed yet — deployment needs a Subgraph Studio account and a deploy key.

```sh
# once, with the key from https://thegraph.com/studio/
bunx graph auth <DEPLOY_KEY>

# then, from this directory
bunx graph deploy morpho-blue-base --network base-sepolia
```

Substitute your Studio subgraph slug for `morpho-blue-base` if it differs. The
resulting subgraph ID feeds `MORPHO_SUBGRAPH_ID` — see `../README.md`.

## Example queries

### What the market-data adapter needs to compute APY

One query returns current TVL plus the two share-price samples the APY math
needs. `SharePriceGrowthToAPY(snapshots[1].sharePriceScaled,
snapshots[0].sharePriceScaled, snapshots[0].timestamp - snapshots[1].timestamp)`
gives APY percent.

```graphql
{
  vaults(
    first: 50
    orderBy: totalAssets
    orderDirection: desc
    where: { totalSupply_gt: "0" }
  ) {
    id
    name
    symbol
    asset
    assetSymbol
    assetDecimals
    totalAssets          # TVL in underlying units -> * price = TVLUsd
    totalSupply
    sharePrice
    sharePriceScaled     # current sample
    fee
    curator
    lastUpdateTimestamp  # -> Venue.UpdatedAt

    # newest first: [0] is "now", pick the one ~24h older as "before"
    snapshots(first: 25, orderBy: hourIndex, orderDirection: desc) {
      hourIndex
      timestamp          # real sample time -> elapsed
      sharePriceScaled
      totalAssets
    }
  }
}
```

Maps onto the normalized `Venue` as:

| Venue field | Source |
|---|---|
| `Chain` | `base-sepolia` (constant) |
| `Project` | `morpho-blue` (constant) |
| `PoolID` | `vault.id` |
| `Symbol` | `vault.assetSymbol` |
| `Asset` | `vault.asset` |
| `TVLUsd` | `totalAssets / 10^assetDecimals * assetPrice` |
| `APYBase` | `SharePriceGrowthToAPY(older, newer, elapsed)` |
| `APYReward` | 0 — MORPHO rewards are distributed off-vault via a URD and are not visible on-chain here |
| `UpdatedAt` | `lastUpdateTimestamp` |

### One vault's share-price history

```graphql
{
  vaults(first: 1, orderBy: totalAssets, orderDirection: desc) {
    name
    symbol
    snapshots(first: 168, orderBy: hourIndex, orderDirection: desc) {
      hourStartTimestamp
      timestamp
      sharePrice
      sharePriceScaled
      totalAssets
      sampleCount
    }
  }
}
```

### Recent TVL flow

```graphql
{
  vaultDeposits(first: 20, orderBy: timestamp, orderDirection: desc) {
    vault { symbol }
    assets
    shares
    owner
    timestamp
    txHash
  }
  vaultWithdraws(first: 20, orderBy: timestamp, orderDirection: desc) {
    vault { symbol }
    assets
    owner
    timestamp
  }
}
```

## Notes and limitations

- `totalAssets` / `totalSupply` are re-read from the vault with `try_` calls on
  every event. A vault that reverts (paused, misconfigured) is skipped for that
  block rather than halting indexing for every other vault.
- Rewards (MORPHO emissions via the Universal Rewards Distributor) are not
  indexed. `sharePriceScaled` growth is the **base** APY only.
- No USD pricing in the subgraph. TVL is in underlying units; the consumer joins
  a price. This keeps the subgraph free of oracle dependencies and price-feed
  staleness, and the consumer already has prices for the other venues.
- Vault-share `decimals` (18 for a USDC vault, thanks to the virtual offset) is
  stored on `Vault.decimals`; the underlying's decimals is `assetDecimals`. Use
  `assetDecimals` for TVL, never `decimals`.

## License

MIT
