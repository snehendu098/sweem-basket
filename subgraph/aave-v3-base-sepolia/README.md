# aave-v3-base-sepolia

Aave V3 reserves on **Base Sepolia (chain 84532)**.

Neither Aave nor anyone else publishes a Base Sepolia subgraph — `aave/protocol-subgraphs`
ships mainnet manifests only. This is written from scratch against the live
testnet deployment, covering exactly what a yield router consumes: the supply
rate, the size of the reserve, the liveness flags and an oracle price.

## Addresses (verified on-chain, not from docs)

| Contract | Address | Deploy block |
|---|---|---|
| Pool | `0x07eA79F68B2B3df564D0A34F8e19D9B1e339814b` | **6719154** |
| PoolConfigurator | `0x347Ae6820F48e9Dd563235742d89FAef6ffCaA72` | **6719154** |
| PoolAddressesProvider | `0xd449fed49d9c443688d6816fe6872f21402e41de` | — |
| AaveOracle (via provider) | `0x29e1ef0209275d0f403e8c57861c2df8706ea244` | — |

Deploy blocks were found by binary search on `eth_getCode` against
`https://sepolia.base.org`, then re-verified on `https://base-sepolia.drpc.org`:
block `N-1` returns `0x`, block `N` returns bytecode. Two independent nodes agree.

`PoolAddressesProvider` is the only address pinned in the mapping — it is the
registry root and immutable by design. The oracle is re-read from it on every
update, because Aave governance can swap the oracle behind the provider.

Two reserves exist on this market:

| Asset | Address | Decimals | aToken |
|---|---|---|---|
| USDC | `0x036CbD53842c5426634e7929541eC2318f3dCF7e` | 6 | `aBasSepUSDC` `0xf53B60F4006cab2b3C4688ce41fD5362427A2A66` |
| WETH | `0x4200000000000000000000000000000000000006` | 18 | `0x96e32de4B1d1617B8c2AE13a88B9cC287239b13f` |

## What it indexes

| Entity | What it is |
|---|---|
| `Reserve` | one underlying asset: rates (ray), supply/borrow totals, utilization, liveness flags, price |
| `PriceOracleAsset` / `PriceOracle` | latest AaveOracle quote and the oracle's base-currency unit (1e8) |
| `ReserveHourlySnapshot` | rate and size sampled into hourly buckets — the history series |

Not indexed: users, positions, borrow-side accounting beyond the totals, e-mode,
isolation mode, incentives/rewards. A router does not read them, and each one is
a schema the Go side would have to learn.

### Discovery: configurator event, not a hardcoded list

`PoolConfigurator.ReserveInitialized` creates the `Reserve` and is the only
place the aToken and variableDebtToken addresses are ever emitted — the Pool
never puts them on an event. A new reserve listed later is picked up with no
code change.

`Pool.ReserveDataUpdated` then carries `liquidityRate`, `variableBorrowRate` and
both indices directly, so the rate never needs a contract call. Everything the
event does *not* carry is re-read with `try_` calls on each update:

- `aToken.totalSupply()` → `totalLiquidity`
- `variableDebtToken.totalSupply()` → `totalCurrentVariableDebt`
- `Pool.getConfiguration(asset)` → `isActive` / `isFrozen` / `isPaused` /
  `borrowingEnabled`, decoded from bits 56 / 57 / 60 / 58 of the configuration
  bitmap
- `AaveOracle.getAssetPrice(asset)` → `price.priceInEth`

Every one is a `try_`. A single reverting reserve is skipped for that block
rather than halting indexing for the whole market.

### Rate units

`liquidityRate` is stored raw: an Aave **ray**, 1e27 == 100% annual simple
interest. That is exactly what `RayRateToAPY()` in
`services/market-data/internal/source/apy.go` takes, so no conversion happens in
the subgraph.

Live check: USDC `liquidityRate` = `12359243175554294189815104` → `/1e27` =
`0.012359` = **1.2359% APR**, matching the value read straight off the Pool.

`priceInEth` keeps Aave's historical misnomer on purpose — on V3 it is the price
in the oracle's base currency (USD) scaled by `oracle.baseCurrencyUnit` (1e8).
The Go adapter already reads it under that name.

## Maps onto the existing Go adapter with no query change

`services/market-data/internal/source/aave_v3.go` issues:

```graphql
{
  reserves(first: 200, where: {isActive: true, isFrozen: false, isPaused: false}) {
    id
    symbol
    decimals
    underlyingAsset
    liquidityRate
    totalLiquidity
    isActive
    isFrozen
    isPaused
    price { priceInEth oracle { baseCurrencyUnit } }
  }
}
```

Every field above exists here with the same name, type and unit. The nested
`price { … oracle { … } }` shape is reproduced deliberately rather than
flattened, so `aave_v3.go` needs no edit at all.

| Venue field | Source |
|---|---|
| `PoolID` / `ID` | `reserve.id` (underlying address) |
| `Symbol` | `reserve.symbol` |
| `Asset` | `reserve.underlyingAsset` |
| `TVLUsd` | `totalLiquidity / 10^decimals * (priceInEth / baseCurrencyUnit)` |
| `APY` / `APYBase` | `RayRateToAPY(liquidityRate)` |
| `APYReward` | 0 — no incentives controller on this testnet market |

Extra fields this exposes beyond the adapter's query, for the history the
adapter cannot get anywhere else: `variableBorrowRate`, `liquidityIndex`,
`utilizationRate`, `availableLiquidity`, `aToken`, `variableDebtToken` and the
`snapshots` series.

## Build

```sh
bun install
bunx graph codegen
bunx graph build --network base-sepolia
```

## Deploy

```sh
bunx graph auth <DEPLOY_KEY>                              # once, key from thegraph.com/studio
bunx graph deploy aave-v-3-base-sepolia --network base-sepolia
```

Substitute your Studio slug for `aave-v3-base-sepolia` if it differs. The
resulting subgraph ID feeds `AAVE_V3_SUBGRAPH_ID` — see `../README.md`.

## Example query — the whole rate history for one reserve

```graphql
{
  reserve(id: "0x036cbd53842c5426634e7929541ec2318f3dcf7e") {
    symbol
    decimals
    liquidityRate
    totalLiquidity
    utilizationRate
    price { priceInEth oracle { baseCurrencyUnit } }
    snapshots(first: 168, orderBy: hourIndex, orderDirection: desc) {
      timestamp
      liquidityRate
      totalLiquidity
      utilizationRate
      priceInEth
    }
  }
}
```

## License

MIT
