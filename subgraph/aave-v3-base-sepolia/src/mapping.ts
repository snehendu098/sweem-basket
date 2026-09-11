import {
  Address,
  BigDecimal,
  BigInt,
  Bytes,
  ethereum,
} from "@graphprotocol/graph-ts";
import { ReserveInitialized } from "../generated/PoolConfigurator/PoolConfigurator";
import {
  Pool,
  ReserveDataUpdated,
} from "../generated/Pool/Pool";
import { PoolAddressesProvider } from "../generated/Pool/PoolAddressesProvider";
import { AaveOracle } from "../generated/Pool/AaveOracle";
import { ERC20 } from "../generated/Pool/ERC20";
import {
  PriceOracle,
  PriceOracleAsset,
  Reserve,
  ReserveHourlySnapshot,
} from "../generated/schema";

const ZERO = BigInt.zero();
const ONE = BigInt.fromI32(1);
const TWO = BigInt.fromI32(2);
const HOUR = 3600;

// PoolAddressesProvider for the Aave V3 Base Sepolia market. Immutable by
// design — it is the registry root, so it is the one address worth pinning.
// Pool 0x07eA79F6… and PoolConfigurator 0x347Ae682… are both resolved from it.
const ADDRESSES_PROVIDER = "0xd449fed49d9c443688d6816fe6872f21402e41de";

// Reserve configuration bitmap layout (Aave V3 ReserveConfiguration.sol).
const ACTIVE_BIT: u8 = 56;
const FROZEN_BIT: u8 = 57;
const BORROWING_BIT: u8 = 58;
const PAUSED_BIT: u8 = 60;

export function handleReserveInitialized(event: ReserveInitialized): void {
  let id = event.params.asset.toHexString();
  let reserve = Reserve.load(id);
  if (reserve == null) {
    reserve = new Reserve(id);
    reserve.underlyingAsset = changetype<Bytes>(event.params.asset);
    reserve.createdAtBlock = event.block.number;
    reserve.createdAtTimestamp = event.block.timestamp;

    let token = ERC20.bind(event.params.asset);
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
    reserve.price = syncOracleAsset(event.params.asset, event).id;
  }

  // The configurator knows the token addresses; the Pool never emits them.
  reserve.pool = changetype<Bytes>(poolAddress());
  reserve.aToken = changetype<Bytes>(event.params.aToken);
  reserve.variableDebtToken = changetype<Bytes>(event.params.variableDebtToken);
  reserve.lastUpdateBlock = event.block.number;
  reserve.lastUpdateTimestamp = event.block.timestamp;
  reserve.save();

  refresh(reserve, event);
}

export function handleReserveDataUpdated(event: ReserveDataUpdated): void {
  let id = event.params.reserve.toHexString();
  let reserve = Reserve.load(id);
  if (reserve == null) {
    // Should not happen — ReserveInitialized runs first — but a reserve added
    // by a future configurator must not be silently dropped.
    reserve = new Reserve(id);
    reserve.underlyingAsset = changetype<Bytes>(event.params.reserve);
    reserve.createdAtBlock = event.block.number;
    reserve.createdAtTimestamp = event.block.timestamp;
    reserve.pool = changetype<Bytes>(event.address);
    reserve.aToken = Bytes.empty();
    reserve.variableDebtToken = Bytes.empty();

    let token = ERC20.bind(event.params.reserve);
    let symbol = token.try_symbol();
    reserve.symbol = symbol.reverted ? "" : symbol.value;
    let name = token.try_name();
    reserve.name = name.reverted ? "" : name.value;
    let decimals = token.try_decimals();
    reserve.decimals = decimals.reverted ? 18 : decimals.value;

    reserve.totalLiquidity = ZERO;
    reserve.totalCurrentVariableDebt = ZERO;
    reserve.availableLiquidity = ZERO;
    reserve.utilizationRate = BigDecimal.zero();
    reserve.isActive = false;
    reserve.isFrozen = false;
    reserve.isPaused = false;
    reserve.borrowingEnabled = false;
    reserve.price = syncOracleAsset(event.params.reserve, event).id;
  }

  reserve.liquidityRate = event.params.liquidityRate;
  reserve.variableBorrowRate = event.params.variableBorrowRate;
  reserve.liquidityIndex = event.params.liquidityIndex;
  reserve.variableBorrowIndex = event.params.variableBorrowIndex;
  reserve.lastUpdateBlock = event.block.number;
  reserve.lastUpdateTimestamp = event.block.timestamp;
  reserve.save();

  refresh(reserve, event);
  writeSnapshot(reserve, event);
}

/**
 * Re-read the parts of reserve state that are not carried on the event: the
 * aToken / debt-token supplies, the configuration flags and the oracle price.
 * Every call is a `try_` — a single misconfigured reserve must not halt
 * indexing for the rest of the market.
 */
function refresh(reserve: Reserve, event: ethereum.Event): void {
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

  let pool = Pool.bind(poolAddress());
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

  syncOracleAsset(Address.fromBytes(reserve.underlyingAsset), event);
  reserve.save();
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
  asset: Address,
  event: ethereum.Event
): PriceOracleAsset {
  let oracle = PriceOracle.load("1");
  if (oracle == null) {
    oracle = new PriceOracle("1");
    oracle.proxyAddress = Bytes.empty();
    oracle.baseCurrencyUnit = ZERO;
  }

  let provider = PoolAddressesProvider.bind(
    Address.fromString(ADDRESSES_PROVIDER)
  );
  let oracleAddress = provider.try_getPriceOracle();
  if (!oracleAddress.reverted) {
    oracle.proxyAddress = changetype<Bytes>(oracleAddress.value);
  }
  oracle.lastUpdateTimestamp = event.block.timestamp;

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
  entry.lastUpdateTimestamp = event.block.timestamp;
  entry.save();
  return entry;
}

function poolAddress(): Address {
  return Address.fromString("0x07eA79F68B2B3df564D0A34F8e19D9B1e339814b");
}

/** Hourly bucket, last write in the hour wins. Same shape as morpho-blue-base. */
function writeSnapshot(reserve: Reserve, event: ethereum.Event): void {
  let hourIndex = event.block.timestamp.toI32() / HOUR;
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

  snap.timestamp = event.block.timestamp;
  snap.block = event.block.number;
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
