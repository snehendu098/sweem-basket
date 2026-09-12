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

const ACTIVE_BIT: u8 = 56;
const FROZEN_BIT: u8 = 57;
const BORROWING_BIT: u8 = 58;
const PAUSED_BIT: u8 = 60;

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
  let registry = aaveRegistry(event.address);

  let id = event.params.reserve.toHexString();
  let reserve = Reserve.load(id);
  if (reserve == null) {
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

function bit(data: BigInt, n: u8): boolean {
  return data.div(TWO.pow(n)).mod(TWO).equals(ONE);
}

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
