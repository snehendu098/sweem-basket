import {
  Address,
  BigDecimal,
  BigInt,
  Bytes,
  ethereum,
} from "@graphprotocol/graph-ts";
import {
  AbsorbDebt,
  BuyCollateral,
  Comet,
  Supply,
  Withdraw,
  WithdrawReserves,
} from "../generated/Comet/Comet";
import { ERC20 } from "../generated/Comet/ERC20";
import {
  BaseToken,
  Market,
  MarketAccounting,
  MarketConfiguration,
  MarketHourlySnapshot,
  Token,
} from "../generated/schema";

const ZERO = BigInt.zero();
const HOUR = 3600;

// The Comet this subgraph indexes. Single-market contract, so this is also the
// Market id. Kept as a constant because the block handler has no event.address.
const COMET = "0x571621Ce60Cebb0c1D442B5afb38B1663C6Bf017";

// Comet rate convention: getSupplyRate returns a PER-SECOND growth rate scaled
// by 1e18 (the "factor scale"). Multiplying by seconds-per-year gives the
// simple APR Compound's own UI reports.
const FACTOR_SCALE = BigDecimal.fromString("1000000000000000000");
const SECONDS_PER_YEAR = BigDecimal.fromString("31536000");
// Comet price feeds are Chainlink-style, 8 decimals.
const PRICE_SCALE = BigDecimal.fromString("100000000");

/** Seeded once at the start block so the Market exists even before the first event. */
export function handleInit(block: ethereum.Block): void {
  sync(block);
}

export function handleSupply(event: Supply): void {
  sync(event.block);
}
export function handleWithdraw(event: Withdraw): void {
  sync(event.block);
}
export function handleAbsorbDebt(event: AbsorbDebt): void {
  sync(event.block);
}
export function handleBuyCollateral(event: BuyCollateral): void {
  sync(event.block);
}
export function handleWithdrawReserves(event: WithdrawReserves): void {
  sync(event.block);
}

/**
 * Read the whole market from the contract and write Market + configuration +
 * accounting + the hourly bucket. Every call is `try_`: a paused Comet reverts
 * on some getters, and that must not halt indexing.
 */
function sync(block: ethereum.Block): void {
  let address = Address.fromString(COMET);
  let id = address.toHexString();
  let comet = Comet.bind(address);

  let market = Market.load(id);
  if (market == null) {
    market = new Market(id);
    market.cometProxy = changetype<Bytes>(address);
    market.creationBlockNumber = block.number;
    market.configuration = id;
    market.accounting = id;
  }
  market.lastUpdateBlock = block.number;
  market.lastUpdateTimestamp = block.timestamp;
  market.save();

  let baseTokenId = syncConfiguration(comet, id, block);
  syncAccounting(comet, id, baseTokenId, block);
  writeSnapshot(id, block);
}

/** Returns the BaseToken id, or "" if the base token could not be read. */
function syncConfiguration(
  comet: Comet,
  id: string,
  block: ethereum.Block
): string {
  let config = MarketConfiguration.load(id);
  if (config == null) {
    config = new MarketConfiguration(id);
    config.market = id;
    config.symbol = "";
    config.name = "";
    config.baseToken = id;
    config.baseScale = ZERO;
    config.baseTrackingSupplySpeed = ZERO;
    config.trackingIndexScale = ZERO;
    config.numAssets = 0;
  }

  let symbol = comet.try_symbol();
  if (!symbol.reverted) {
    config.symbol = symbol.value;
  }
  let name = comet.try_name();
  if (!name.reverted) {
    config.name = name.value;
  }
  let baseScale = comet.try_baseScale();
  if (!baseScale.reverted) {
    config.baseScale = baseScale.value;
  }
  let speed = comet.try_baseTrackingSupplySpeed();
  if (!speed.reverted) {
    config.baseTrackingSupplySpeed = speed.value;
  }
  let trackingScale = comet.try_trackingIndexScale();
  if (!trackingScale.reverted) {
    config.trackingIndexScale = trackingScale.value;
  }
  let numAssets = comet.try_numAssets();
  if (!numAssets.reverted) {
    config.numAssets = numAssets.value;
  }

  let base = comet.try_baseToken();
  if (!base.reverted) {
    config.baseToken = syncBaseToken(comet, base.value);
  }
  config.save();
  return config.baseToken;
}

function syncBaseToken(comet: Comet, tokenAddress: Address): string {
  let id = tokenAddress.toHexString();

  let token = Token.load(id);
  if (token == null) {
    token = new Token(id);
    token.address = changetype<Bytes>(tokenAddress);
    let erc20 = ERC20.bind(tokenAddress);
    let symbol = erc20.try_symbol();
    token.symbol = symbol.reverted ? "" : symbol.value;
    let name = erc20.try_name();
    token.name = name.reverted ? "" : name.value;
    let decimals = erc20.try_decimals();
    token.decimals = decimals.reverted ? 18 : decimals.value;
    token.save();
  }

  let baseToken = BaseToken.load(id);
  if (baseToken == null) {
    baseToken = new BaseToken(id);
    baseToken.token = id;
    baseToken.priceFeed = Bytes.empty();
    baseToken.priceUsd = BigDecimal.zero();
  }

  let feed = comet.try_baseTokenPriceFeed();
  if (!feed.reverted) {
    baseToken.priceFeed = changetype<Bytes>(feed.value);
    let price = comet.try_getPrice(feed.value);
    if (!price.reverted) {
      baseToken.priceUsd = price.value.toBigDecimal().div(PRICE_SCALE);
    }
  }
  baseToken.save();
  return id;
}

function syncAccounting(
  comet: Comet,
  id: string,
  baseTokenId: string,
  block: ethereum.Block
): void {
  let acc = MarketAccounting.load(id);
  if (acc == null) {
    acc = new MarketAccounting(id);
    acc.market = id;
    acc.totalBaseSupply = ZERO;
    acc.totalBaseBorrow = ZERO;
    acc.totalBaseSupplyUsd = BigDecimal.zero();
    acc.totalBaseBorrowUsd = BigDecimal.zero();
    acc.reserves = ZERO;
    acc.utilization = ZERO;
    acc.supplyRatePerSecond = ZERO;
    acc.borrowRatePerSecond = ZERO;
    acc.supplyApr = BigDecimal.zero();
    acc.borrowApr = BigDecimal.zero();
    acc.rewardSupplyApr = BigDecimal.zero();
    acc.netSupplyApr = BigDecimal.zero();
  }

  let supply = comet.try_totalSupply();
  if (!supply.reverted) {
    acc.totalBaseSupply = supply.value;
  }
  let borrow = comet.try_totalBorrow();
  if (!borrow.reverted) {
    acc.totalBaseBorrow = borrow.value;
  }
  let reserves = comet.try_getReserves();
  if (!reserves.reverted) {
    acc.reserves = reserves.value;
  }

  // Comet publishes no rate event; the rate is a pure function of utilization,
  // so read utilization and price the curve at it.
  let utilization = comet.try_getUtilization();
  if (!utilization.reverted) {
    acc.utilization = utilization.value;

    let supplyRate = comet.try_getSupplyRate(utilization.value);
    if (!supplyRate.reverted) {
      acc.supplyRatePerSecond = supplyRate.value;
      acc.supplyApr = perSecondToApr(supplyRate.value);
    }
    let borrowRate = comet.try_getBorrowRate(utilization.value);
    if (!borrowRate.reverted) {
      acc.borrowRatePerSecond = borrowRate.value;
      acc.borrowApr = perSecondToApr(borrowRate.value);
    }
  }

  // COMP emissions are not valued here — see README. rewardSupplyApr stays 0,
  // so netSupplyApr == supplyApr and the consumer's fallback is a no-op.
  acc.netSupplyApr = acc.supplyApr.plus(acc.rewardSupplyApr);

  let priceUsd = BigDecimal.zero();
  let scale = BigDecimal.zero();
  let baseToken = BaseToken.load(baseTokenId);
  if (baseToken != null) {
    priceUsd = baseToken.priceUsd;
    let token = Token.load(baseToken.token);
    if (token != null) {
      scale = BigInt.fromI32(10).pow(token.decimals as u8).toBigDecimal();
    }
  }
  if (scale.gt(BigDecimal.zero())) {
    acc.totalBaseSupplyUsd = acc.totalBaseSupply
      .toBigDecimal()
      .div(scale)
      .times(priceUsd);
    acc.totalBaseBorrowUsd = acc.totalBaseBorrow
      .toBigDecimal()
      .div(scale)
      .times(priceUsd);
  }

  acc.lastUpdateBlock = block.number;
  acc.lastUpdateTimestamp = block.timestamp;
  acc.save();
}

/** per-second rate scaled 1e18 -> simple annual rate as a decimal fraction. */
function perSecondToApr(rate: BigInt): BigDecimal {
  return rate.toBigDecimal().div(FACTOR_SCALE).times(SECONDS_PER_YEAR);
}

function writeSnapshot(id: string, block: ethereum.Block): void {
  let acc = MarketAccounting.load(id);
  if (acc == null) {
    return;
  }

  let hourIndex = block.timestamp.toI32() / HOUR;
  let snapId = id + "-" + hourIndex.toString();

  let snap = MarketHourlySnapshot.load(snapId);
  if (snap == null) {
    snap = new MarketHourlySnapshot(snapId);
    snap.market = id;
    snap.hourIndex = hourIndex;
    snap.hourStartTimestamp = BigInt.fromI32(hourIndex).times(
      BigInt.fromI32(HOUR)
    );
    snap.sampleCount = 0;
  }

  snap.timestamp = block.timestamp;
  snap.block = block.number;
  snap.totalBaseSupply = acc.totalBaseSupply;
  snap.totalBaseBorrow = acc.totalBaseBorrow;
  snap.totalBaseSupplyUsd = acc.totalBaseSupplyUsd;
  snap.utilization = acc.utilization;
  snap.supplyRatePerSecond = acc.supplyRatePerSecond;
  snap.supplyApr = acc.supplyApr;
  snap.netSupplyApr = acc.netSupplyApr;

  let config = MarketConfiguration.load(id);
  let price = BigDecimal.zero();
  if (config != null) {
    let baseToken = BaseToken.load(config.baseToken);
    if (baseToken != null) {
      price = baseToken.priceUsd;
    }
  }
  snap.baseTokenPriceUsd = price;

  snap.sampleCount = snap.sampleCount + 1;
  snap.save();
}
