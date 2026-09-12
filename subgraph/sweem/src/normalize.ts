import {
  Address,
  BigDecimal,
  BigInt,
  Bytes,
  dataSource,
  ethereum,
} from "@graphprotocol/graph-ts";
import { AssetVenues, RateFeed, Venue } from "../generated/schema";

export const ZERO = BigInt.zero();
export const SECONDS_PER_YEAR: f64 = 31536000.0;

// Scaled i64, not f64.toString(): AssemblyScript renders small doubles in
// exponent form ("1.2e-7"), which BigDecimal.fromString reads as zero.
const APY_DECIMALS: f64 = 1.0e12;
const APY_DECIMALS_BD = BigDecimal.fromString("1000000000000");

const MAX_APY_PERCENT: f64 = 1.0e6;

function chainLabel(): string {
  let network = dataSource.network();
  if (network == "base" || network == "base-sepolia") {
    return "Base";
  }
  return network;
}

function venueId(protocol: string, pool: Address): string {
  return chainLabel() + ":" + protocol + ":" + pool.toHexString();
}

function percent(value: f64): BigDecimal {
  if (!isFinite(value) || value <= 0.0) {
    return BigDecimal.zero();
  }
  if (value > MAX_APY_PERCENT) {
    value = MAX_APY_PERCENT;
  }
  let scaled = BigInt.fromI64(<i64>Math.round(value * APY_DECIMALS));
  return BigDecimal.fromString(scaled.toString()).div(APY_DECIMALS_BD);
}

// Expm1(n*Log1p(r)) not Pow: at r = apr/31536000 ~ 1e-9, `1 + r` loses most of
// r's significant digits in f64.
function aprToApyPercent(apr: f64): BigDecimal {
  if (!(apr > 0.0)) {
    return BigDecimal.zero();
  }
  return percent(
    Math.expm1(SECONDS_PER_YEAR * Math.log1p(apr / SECONDS_PER_YEAR)) * 100.0
  );
}

export function rayRateToApyPercent(rate: BigInt): BigDecimal {
  return aprToApyPercent(toFloat(rate) / 1.0e27);
}

export function perSecondRateToApyPercent(rate: BigInt): BigDecimal {
  return aprToApyPercent((toFloat(rate) / 1.0e18) * SECONDS_PER_YEAR);
}

export function sharePriceGrowthToApyPercent(
  before: BigInt,
  after: BigInt,
  elapsedSeconds: BigInt
): BigDecimal {
  if (before.le(ZERO) || elapsedSeconds.le(ZERO)) {
    return BigDecimal.zero();
  }
  let b = toFloat(before);
  let a = toFloat(after);
  if (!(b > 0.0)) {
    return BigDecimal.zero();
  }
  let growth = a / b - 1.0;
  if (!(growth > 0.0)) {
    return BigDecimal.zero();
  }
  let periods = SECONDS_PER_YEAR / <f64>elapsedSeconds.toI64();
  return percent(Math.expm1(periods * Math.log1p(growth)) * 100.0);
}

function toFloat(value: BigInt): f64 {
  return parseFloat(value.toString());
}

export function upsertVenue(
  protocol: string,
  pool: Address,
  symbol: string,
  asset: Address,
  assetSymbol: string,
  assetDecimals: i32,
  supplyApy: BigDecimal,
  totalSupply: BigInt,
  utilization: BigDecimal | null,
  isActive: boolean,
  block: ethereum.Block
): void {
  let id = venueId(protocol, pool);
  let venue = Venue.load(id);
  if (venue == null) {
    venue = new Venue(id);
    venue.chain = chainLabel();
    venue.protocol = protocol;
    venue.pool = changetype<Bytes>(pool);
  }
  venue.symbol = symbol;
  venue.asset = changetype<Bytes>(asset);
  venue.assetSymbol = assetSymbol;
  venue.assetDecimals = assetDecimals;
  venue.supplyApy = supplyApy;
  applyIntrinsic(venue);
  venue.totalSupply = totalSupply;
  venue.utilization = utilization;
  venue.isActive = isActive;
  venue.lastUpdateBlock = block.number;
  venue.lastUpdateTimestamp = block.timestamp;
  venue.save();
  indexByAsset(venue);
}

export function applyIntrinsic(venue: Venue): void {
  let feed = RateFeed.load(venue.asset.toHexString());
  if (feed == null || !feed.available) {
    venue.intrinsicApy = BigDecimal.zero();
    venue.intrinsicApyAvailable = false;
  } else {
    venue.intrinsicApy = feed.intrinsicApy;
    venue.intrinsicApyAvailable = true;
  }
  venue.totalApy = venue.supplyApy.plus(venue.intrinsicApy);
}

function indexByAsset(venue: Venue): void {
  let key = venue.asset.toHexString();
  let index = AssetVenues.load(key);
  if (index == null) {
    index = new AssetVenues(key);
    index.venueIds = [];
  }
  let ids = index.venueIds;
  for (let i = 0; i < ids.length; i++) {
    if (ids[i] == venue.id) {
      return;
    }
  }
  ids.push(venue.id);
  index.venueIds = ids;
  index.save();
}

export function refreshVenuesForAsset(asset: Address): void {
  let index = AssetVenues.load(asset.toHexString());
  if (index == null) {
    return;
  }
  let ids = index.venueIds;
  for (let i = 0; i < ids.length; i++) {
    let venue = Venue.load(ids[i]);
    if (venue == null) {
      continue;
    }
    applyIntrinsic(venue);
    venue.save();
  }
}
