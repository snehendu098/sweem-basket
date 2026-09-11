# compound-v3-base-sepolia

Compound III (Comet) **cUSDCv3** on **Base Sepolia (chain 84532)**.

Compound publishes no Base Sepolia subgraph. This one exists because of a
specific gap: **Comet emits no rate event at all.** There is no `AccrueInterest`,
no `NewSupplyRate` — nothing on any log carries the supply rate. The rate is only
readable from `getSupplyRate(getUtilization())`, so an indexer has to call the
contract and store the result, or the rate has no history anywhere.

## Addresses (verified on-chain)

| Contract | Address | Deploy block |
|---|---|---|
| Comet (cUSDCv3) | `0x571621Ce60Cebb0c1D442B5afb38B1663C6Bf017` | **7646646** |
| base token (USDC, 6dp) | `0x036CbD53842c5426634e7929541eC2318f3dCF7e` | — |
| base token price feed | `0xDC6d86db02E198764B077e1af7B1d31BbF575508` | — |

Deploy block found by binary search on `eth_getCode` against
`https://sepolia.base.org` and independently reproduced on
`https://base-sepolia.drpc.org` (block 7646645 → `0x`, 7646646 → bytecode).

## What it indexes

Comet is a **single-market contract**, not a pool of reserves — one Comet, one
base asset, one supply rate. So there is exactly one `Market`, and its id is the
Comet address.

| Entity | What it is |
|---|---|
| `Market` | the Comet itself; points at configuration and accounting |
| `MarketConfiguration` | symbol, name, base token, base scale, COMP tracking speed, collateral count |
| `BaseToken` / `Token` | the base asset and its Comet price feed quote |
| `MarketAccounting` | supply/borrow totals, USD totals, utilization, per-second rates and the APRs |
| `MarketHourlySnapshot` | the same numbers bucketed hourly — the rate history |

Collateral assets, user positions and liquidations are not indexed. Only the
base asset earns supply yield in Comet, so collateral is not a routable venue.

### Why event-driven refresh is exact here, not an approximation

Comet's supply rate is a **pure function of utilization**, and utilization only
changes when the base balance or the borrow balance changes. Those changes are
exactly the events indexed:

`Supply`, `Withdraw`, `AbsorbDebt`, `BuyCollateral`, `WithdrawReserves`

So between two consecutive handler runs, the stored rate is not stale — it is
the rate that actually applied over that interval. A polling block handler would
add cost without adding information.

A `blockHandlers` entry with `filter: { kind: once }` runs at the start block, so
the `Market` row exists with a real rate from the first indexed block even before
any event fires.

### Rate units — this is the one thing to get right

`getSupplyRate` returns a **per-second** growth rate scaled by **1e18**. It is
not per block, and Base's 2-second blocks are irrelevant to it.

```
supplyApr = supplyRatePerSecond / 1e18 * 31_536_000     # plain decimal fraction
```

Live check: `getSupplyRate(getUtilization())` = `8113627` →
`8113627 / 1e18 * 31536000` = `0.00025587` = **0.0256% APR**.

`supplyApr` / `netSupplyApr` are stored as that plain decimal fraction, matching
Compound's own convention and matching what `compound_v3.go` already parses.

**On `apy.go`:** use `APRToAPY(netSupplyApr)`, which is what the adapter already
does. Do **not** use `PerBlockRateToAPY` — that helper expects a per-*block*
rate plus a blocks-per-year count, and Comet's rate is per-second.
`PerSecondMantissaToAPY(supplyRatePerSecond, 1e18)` in the same file is the
direct equivalent if you would rather feed the raw integer than the derived
fraction; both land on the same number, since `APRToAPY` compounds per second.

### `rewardSupplyApr` is always 0 — deliberately

`baseTrackingSupplySpeed` is non-zero on this deployment (`11574074074`), so COMP
is nominally accruing. Turning that into an APR needs a **COMP/USD price**, and
Base Sepolia has no trustworthy COMP feed — a testnet COMP price is a made-up
number, and a made-up reward APR misroutes real money on mainnet the moment the
same code path runs there.

So `rewardSupplyApr = 0` and `netSupplyApr = supplyApr`. The raw
`baseTrackingSupplySpeed` and `trackingIndexScale` are exposed on
`MarketConfiguration` so a consumer that *does* have a COMP price can compute it:

```
rewardSupplyApr = speed / trackingIndexScale * 31_536_000 * compPriceUsd / totalBaseSupplyUsd
```

The Go adapter already falls back to `supplyApr` when `netSupplyApr <= 0`, so
this costs nothing there.

## Maps onto the existing Go adapter with no query change

`services/market-data/internal/source/compound_v3.go` issues:

```graphql
{
  markets(first: 50) {
    id
    configuration { symbol baseToken { token { address symbol decimals } } }
    accounting { totalBaseSupplyUsd supplyApr rewardSupplyApr netSupplyApr }
  }
}
```

Every field exists here with that exact name, nesting and unit. The
`configuration { baseToken { token { … } } }` nesting is reproduced deliberately
rather than flattened so the adapter needs no edit.

| Venue field | Source |
|---|---|
| `PoolID` / `ID` | `market.id` (Comet address) |
| `Symbol` | `configuration.symbol` → `cUSDCv3` |
| `Asset` | `configuration.baseToken.token.address` |
| `TVLUsd` | `accounting.totalBaseSupplyUsd` (already USD) |
| `APY` | `APRToAPY(netSupplyApr)` |
| `APYBase` | `APRToAPY(supplyApr)` |
| `APYReward` | `APY - APYBase` → 0, see above |

**One divergence to be aware of:** `totalBaseSupplyUsd` is priced from Comet's
own `getPrice(baseTokenPriceFeed())`, which on Base Sepolia is a mock feed
returning a flat `1e8` (= $1.00) for USDC. That is correct for a stablecoin and
wrong in general — on mainnet the same code reads the real Chainlink feed Comet
is configured with, so no change is needed, but do not read this field as a
market price on testnet.

The adapter drops any market with `tvl <= 0`. Current supply is ~139,723 USDC,
so this market is well above zero; a market that is genuinely empty will
correctly be dropped.

## Build

```sh
bun install
bunx graph codegen
bunx graph build --network base-sepolia
```

## Deploy

```sh
bunx graph auth <DEPLOY_KEY>                                    # once
bunx graph deploy compound-v-3-base-sepolia --network base-sepolia
```

The resulting subgraph ID feeds `COMPOUND_V3_SUBGRAPH_ID` — see `../README.md`.

## Example query — supply rate history

```graphql
{
  market(id: "0x571621ce60cebb0c1d442b5afb38b1663c6bf017") {
    configuration { symbol baseToken { token { symbol decimals } } }
    accounting {
      totalBaseSupply
      totalBaseSupplyUsd
      utilization
      supplyRatePerSecond
      supplyApr
      netSupplyApr
      lastUpdateTimestamp
    }
    snapshots(first: 168, orderBy: hourIndex, orderDirection: desc) {
      timestamp
      supplyApr
      utilization
      totalBaseSupplyUsd
    }
  }
}
```

## License

MIT
