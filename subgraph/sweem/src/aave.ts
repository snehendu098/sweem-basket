import {
  Address,
  BigDecimal,
  BigInt,
  Bytes,
  dataSource,
  ethereum,
} from "@graphprotocol/graph-ts";
import { ReserveInitialized } from "../generated/PoolConfigurator/PoolConfigurator";
import { Pool, ReserveDataUpdated } from "../generated/Pool/Pool";
import { PoolAddressesProvider } from "../generated/Pool/PoolAddressesProvider";
import { AaveOracle } from "../generated/Pool/AaveOracle";
import { AToken } from "../generated/Pool/AToken";
import { ERC20 } from "../generated/Pool/ERC20";
import {
  AaveRegistry,
  PriceOracle,
  PriceOracleAsset,
  Reserve,
  ReserveHourlySnapshot,
} from "../generated/schema";
import { ZERO, rayRateToApyPercent, upsertVenue } from "./normalize";

const ONE = BigInt.fromI32(1);
const TWO = BigInt.fromI32(2);
const HOUR = 3600;

export const PROTOCOL = "aave-v3";

// Reserve configuration bitmap layout (Aave V3 ReserveConfiguration.sol).
const ACTIVE_BIT: u8 = 56;
const FROZEN_BIT: u8 = 57;
const BORROWING_BIT: u8 = 58;
const PAUSED_BIT: u8 = 60;

/**
 * Seed every reserve from `Pool.getReservesList()` at the start block.
 *
 * This is what lets the mainnet deployment start near chain head. Historically
 * the aToken and variableDebtToken addresses arrived only on `ReserveInitialized`,
 * which fires once at market launch — block 2357134 on Base, ~49M blocks of
 * scanning ago. Reading the reserve list and the token addresses directly costs
 * a handful of eth_calls, once, and removes the dependency entirely.
 *
 * Rates are not seeded here: `liquidityRate` lives on the event. Every reserve
 * with any activity emits `ReserveDataUpdated` within minutes, so a Venue is
 * complete long before the subgraph reaches head. A reserve with no activity in
 * the whole window has no rate to route on anyway.
 */
export function handlePoolInit(block: ethereum.Block): void {
  let poolAddress = dataSource.address();
  let registry = aaveRegistry(poolAddress);

  let list = Pool.bind(poolAddress).try_getReservesList();
  if (list.reverted) {
    return;
  }

  let assets = list.value;
  for (let i = 0; i < assets.length; i++) {
    let asset = assets[i];
    let id = asset.toHexString();
    let reserve = Reserve.load(id);
    if (reserve == null) {
      reserve = newReserve(id, asset, block);
      reserve.price = syncOracleAsset(registry, asset, block).id;
    }
    reserve.pool = registry.pool;
    reserve.lastUpdateBlock = block.number;
    reserve.lastUpdateTimestamp = block.timestamp;
    reserve.save();
    refresh(registry, reserve, block);
  }
}

export function handleReserveInitialized(event: ReserveInitialized): void {
  // The configurator's events do not carry the Pool, but the aToken knows it.
  // Resolving it here rather than hardcoding is what lets one mapping serve
  // both Base Sepolia and Base mainnet.
  let registry = AaveRegistry.load("1");
  if (registry == null) {
    let pool = AToken.bind(event.params.aToken).try_POOL();
    if (!pool.reverted) {
      registry = aaveRegistry(pool.value);
    }
  }

  let id = event.params.asset.toHexString();
  let reserve = Reserve.load(id);
  if (reserve == null) {
    reserve = newReserve(id, event.params.asset, event.block);
    reserve.price = syncOracleAsset(registry, event.params.asset, event.block).id;
  }

  reserve.pool =
    registry == null ? Bytes.empty() : registry.pool;
  reserve.aToken = changetype<Bytes>(event.params.aToken);
  reserve.variableDebtToken = changetype<Bytes>(event.params.variableDebtToken);
  reserve.lastUpdateBlock = event.block.number;
  reserve.lastUpdateTimestamp = event.block.timestamp;
  reserve.save();

  refresh(registry, reserve, event.block);
}

export function handleReserveDataUpdated(event: ReserveDataUpdated): void {
  // In a Pool handler event.address IS the Pool, which is the authoritative
  // way to learn it on whichever network this is running against.
  let registry = aaveRegistry(event.address);

  let id = event.params.reserve.toHexString();
  let reserve = Reserve.load(id);
  if (reserve == null) {
    // Should not happen — ReserveInitialized runs first — but a reserve added
    // by a future configurator must not be silently dropped.
    reserve = newReserve(id, event.params.reserve, event.block);
    reserve.pool = changetype<Bytes>(event.address);
    reserve.price = syncOracleAsset(registry, event.params.reserve, event.block).id;
  }

  reserve.liquidityRate = event.params.liquidityRate;
  reserve.variableBorrowRate = event.params.variableBorrowRate;
  reserve.liquidityIndex = event.params.liquidityIndex;
  reserve.variableBorrowIndex = event.params.variableBorrowIndex;
  reserve.lastUpdateBlock = event.block.number;
  reserve.lastUpdateTimestamp = event.block.timestamp;
  reserve.save();

  refresh(registry, reserve, event.block);
  writeSnapshot(reserve, event.block);
}

function newReserve(
  id: string,
  asset: Address,
  block: ethereum.Block
): Reserve {
  let reserve = new Reserve(id);
  reserve.underlyingAsset = changetype<Bytes>(asset);
  reserve.createdAtBlock = block.number;
  reserve.createdAtTimestamp = block.timestamp;
  reserve.pool = Bytes.empty();
  reserve.aToken = Bytes.empty();
  reserve.variableDebtToken = Bytes.empty();

  let token = ERC20.bind(asset);
  let symbol = token.try_symbol();
  reserve.symbol = symbol.reverted ? "" : symbol.value;
  let name = token.try_name();
  reserve.name = name.reverted ? "" : name.value;
  let decimals = token.try_decimals();
  reserve.decimals = decimals.reverted ? 18 : decimals.value;

  reserve.liquidityRate = ZERO;
  reserve.variableBorrowRate = ZERO;
  reserve.liquidityIndex = ZERO;
  reserve.variableBorrowIndex = ZERO;
  reserve.totalLiquidity = ZERO;
  reserve.totalCurrentVariableDebt = ZERO;
  reserve.availableLiquidity = ZERO;
  reserve.utilizationRate = BigDecimal.zero();
  reserve.isActive = false;
  reserve.isFrozen = false;
  reserve.isPaused = false;
  reserve.borrowingEnabled = false;
  return reserve;
}

/**
 * Singleton holding this network's Pool and PoolAddressesProvider. Written the
 * first time an address is learned and read from cache afterwards, so the two
 * extra eth_calls happen once per subgraph rather than once per event.
 */
function aaveRegistry(pool: Address): AaveRegistry {
  let registry = AaveRegistry.load("1");
  if (registry != null) {
    return registry;
  }

  registry = new AaveRegistry("1");
  registry.pool = changetype<Bytes>(pool);
  registry.addressesProvider = Bytes.empty();

  let provider = Pool.bind(pool).try_ADDRESSES_PROVIDER();
  if (!provider.reverted) {
    registry.addressesProvider = changetype<Bytes>(provider.value);
  }
  registry.save();
  return registry;
}

/**
 * Re-read the parts of reserve state that are not carried on the event: the
 * aToken / debt-token supplies, the configuration flags and the oracle price,
 * then publish the normalized Venue row.
 *
 * Every call is a `try_` — a single misconfigured reserve must not halt
 * indexing for the rest of the market.
 */
function refresh(
  registry: AaveRegistry | null,
  reserve: Reserve,
  block: ethereum.Block
): void {
  ensureTokens(registry, reserve);

  if (reserve.aToken.length == 20) {
    let aToken = ERC20.bind(Address.fromBytes(reserve.aToken));
    let supply = aToken.try_totalSupply();
    if (!supply.reverted) {
      reserve.totalLiquidity = supply.value;
    }
  }
  if (reserve.variableDebtToken.length == 20) {
    let debtToken = ERC20.bind(Address.fromBytes(reserve.variableDebtToken));
    let debt = debtToken.try_totalSupply();
    if (!debt.reverted) {
      reserve.totalCurrentVariableDebt = debt.value;
    }
  }

  reserve.availableLiquidity = reserve.totalLiquidity.minus(
    reserve.totalCurrentVariableDebt
  );
  reserve.utilizationRate = reserve.totalLiquidity.equals(ZERO)
    ? BigDecimal.zero()
    : reserve.totalCurrentVariableDebt
        .toBigDecimal()
        .div(reserve.totalLiquidity.toBigDecimal());

  if (registry != null && registry.pool.length == 20) {
    let pool = Pool.bind(Address.fromBytes(registry.pool));
    let config = pool.try_getConfiguration(
      Address.fromBytes(reserve.underlyingAsset)
    );
    if (!config.reverted) {
      let data = config.value.data;
      reserve.isActive = bit(data, ACTIVE_BIT);
      reserve.isFrozen = bit(data, FROZEN_BIT);
      reserve.borrowingEnabled = bit(data, BORROWING_BIT);
      reserve.isPaused = bit(data, PAUSED_BIT);
    }
  }

  syncOracleAsset(registry, Address.fromBytes(reserve.underlyingAsset), block);
  reserve.save();

  // Aave addresses a supply position by its underlying asset, so the asset is
  // also the pool key the executor allowlist uses.
  let asset = Address.fromBytes(reserve.underlyingAsset);
  upsertVenue(
    PROTOCOL,
    asset,
    reserve.symbol,
    asset,
    reserve.symbol,
    reserve.decimals,
    rayRateToApyPercent(reserve.liquidityRate),
    reserve.totalLiquidity,
    reserve.utilizationRate,
    reserve.isActive && !reserve.isFrozen && !reserve.isPaused,
    block
  );
}

/**
 * Fill in the aToken / variableDebtToken addresses for a reserve that was never
 * seen being initialized — the normal case on a deployment that starts near
 * chain head. `getReserveAToken` / `getReserveVariableDebtToken` exist from Aave
 * 3.2 onwards (present on Base mainnet, absent on the older Base Sepolia Pool),
 * so both are `try_` and a revert simply leaves the addresses to arrive on
 * `ReserveInitialized` the way they always did.
 *
 * Without these two supplies the reserve reports zero TVL, which is a silent
 * mispricing rather than a visible failure — so it is worth re-checking on every
 * refresh until they are known, not just once.
 */
function ensureTokens(registry: AaveRegistry | null, reserve: Reserve): void {
  if (reserve.aToken.length == 20 && reserve.variableDebtToken.length == 20) {
    return;
  }
  if (registry == null || registry.pool.length != 20) {
    return;
  }

  let pool = Pool.bind(Address.fromBytes(registry.pool));
  let asset = Address.fromBytes(reserve.underlyingAsset);

  if (reserve.aToken.length != 20) {
    let aToken = pool.try_getReserveAToken(asset);
    if (!aToken.reverted) {
      reserve.aToken = changetype<Bytes>(aToken.value);
    }
  }
  if (reserve.variableDebtToken.length != 20) {
    let debtToken = pool.try_getReserveVariableDebtToken(asset);
    if (!debtToken.reverted) {
      reserve.variableDebtToken = changetype<Bytes>(debtToken.value);
    }
  }
}

/** Bit `n` of the reserve configuration bitmap, via div/mod so no shift op is needed. */
function bit(data: BigInt, n: u8): boolean {
  return data.div(TWO.pow(n)).mod(TWO).equals(ONE);
}

/**
 * Refresh the oracle singleton and this asset's quote. The oracle is read from
 * the addresses provider on every call rather than pinned, because Aave
 * governance can and does swap the oracle behind the provider.
 */
function syncOracleAsset(
  registry: AaveRegistry | null,
  asset: Address,
  block: ethereum.Block
): PriceOracleAsset {
  let oracle = PriceOracle.load("1");
  if (oracle == null) {
    oracle = new PriceOracle("1");
    oracle.proxyAddress = Bytes.empty();
    oracle.baseCurrencyUnit = ZERO;
  }

  if (registry != null && registry.addressesProvider.length == 20) {
    let provider = PoolAddressesProvider.bind(
      Address.fromBytes(registry.addressesProvider)
    );
    let oracleAddress = provider.try_getPriceOracle();
    if (!oracleAddress.reverted) {
      oracle.proxyAddress = changetype<Bytes>(oracleAddress.value);
    }
  }
  oracle.lastUpdateTimestamp = block.timestamp;

  let entry = PriceOracleAsset.load(asset.toHexString());
  if (entry == null) {
    entry = new PriceOracleAsset(asset.toHexString());
    entry.priceInEth = ZERO;
    entry.oracle = "1";
  }

  if (oracle.proxyAddress.length == 20) {
    let feed = AaveOracle.bind(Address.fromBytes(oracle.proxyAddress));
    let unit = feed.try_BASE_CURRENCY_UNIT();
    if (!unit.reverted) {
      oracle.baseCurrencyUnit = unit.value;
    }
    let price = feed.try_getAssetPrice(asset);
    if (!price.reverted) {
      entry.priceInEth = price.value;
    }
  }

  oracle.save();
  entry.lastUpdateTimestamp = block.timestamp;
  entry.save();
  return entry;
}

/** Hourly bucket, last write in the hour wins. Same shape as the other two protocols. */
function writeSnapshot(reserve: Reserve, block: ethereum.Block): void {
  let hourIndex = block.timestamp.toI32() / HOUR;
  let id = reserve.id + "-" + hourIndex.toString();

  let snap = ReserveHourlySnapshot.load(id);
  if (snap == null) {
    snap = new ReserveHourlySnapshot(id);
    snap.reserve = reserve.id;
    snap.hourIndex = hourIndex;
    snap.hourStartTimestamp = BigInt.fromI32(hourIndex).times(
      BigInt.fromI32(HOUR)
    );
    snap.sampleCount = 0;
  }

  snap.timestamp = block.timestamp;
  snap.block = block.number;
  snap.liquidityRate = reserve.liquidityRate;
  snap.variableBorrowRate = reserve.variableBorrowRate;
  snap.liquidityIndex = reserve.liquidityIndex;
  snap.totalLiquidity = reserve.totalLiquidity;
  snap.totalCurrentVariableDebt = reserve.totalCurrentVariableDebt;
  snap.utilizationRate = reserve.utilizationRate;

  let price = PriceOracleAsset.load(reserve.id);
  snap.priceInEth = price == null ? ZERO : price.priceInEth;

  snap.sampleCount = snap.sampleCount + 1;
  snap.save();
}
