import {
  Address,
  BigDecimal,
  BigInt,
  Bytes,
  dataSource,
  ethereum,
} from "@graphprotocol/graph-ts";
import { AssetVenues, RateFeed, Venue } from "../generated/schema";

// ---------------------------------------------------------------------------
// The normalization layer.
//
// Every protocol expresses yield in its own unit — an Aave ray, a Comet
// per-second 1e18 factor, an ERC-4626 share price with no rate at all. This
// file is the single place those become one comparable number, and it is the
// mirror of services/market-data/internal/source/apy.go. If the two ever
// disagree, one of them is a bug.
// ---------------------------------------------------------------------------

export const ZERO = BigInt.zero();
export const SECONDS_PER_YEAR: f64 = 31536000.0;

// supplyApy is stored to 12 decimal places. Going through a scaled i64 rather
// than f64.toString() is deliberate: AssemblyScript renders small doubles in
// exponent form ("1.2e-7") and BigDecimal.fromString does not read that back.
const APY_DECIMALS: f64 = 1.0e12;
const APY_DECIMALS_BD = BigDecimal.fromString("1000000000000");

// Anything past this is not a yield, it is a broken oracle or an empty vault
// annualized into nonsense. Capping also keeps the i64 scaling below overflow.
const MAX_APY_PERCENT: f64 = 1.0e6;

/**
 * The chain label the router keys venues by. Both Base networks report as
 * `Base`, matching CHAINS=Base on the Go side and the executor allowlist in
 * executor/venues.json — chain id, not chain name, distinguishes testnet.
 */
export function chainLabel(): string {
  let network = dataSource.network();
  if (network == "base" || network == "base-sepolia") {
    return "Base";
  }
  return network;
}

/** `<chain>:<protocol>:<pool>`, byte-for-byte the executor allowlist key. */
export function venueId(protocol: string, pool: Address): string {
  return chainLabel() + ":" + protocol + ":" + pool.toHexString();
}

/** f64 percentage -> BigDecimal, clamped and rounded to 12dp. */
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

/**
 * A simple annual rate (0.0525 == 5.25% APR) compounded every second into an
 * APY percentage (5.25 == 5.25%).
 *
 * Expm1(n * Log1p(r)) rather than (1 + r)^n - 1: with r = apr/31536000 ≈ 1e-9,
 * `1 + r` throws away most of r's significant digits in f64.
 */
export function aprToApyPercent(apr: f64): BigDecimal {
  if (!(apr > 0.0)) {
    return BigDecimal.zero();
  }
  return percent(
    Math.expm1(SECONDS_PER_YEAR * Math.log1p(apr / SECONDS_PER_YEAR)) * 100.0
  );
}

/**
 * Aave: `liquidityRate` is a ray (1e27 == 100%) annual simple supply rate.
 * Verified on Base Sepolia WETH: 704152479263146270890280563 / 1e27 = 0.704
 * (70.4% APR) -> 102.2% APY.
 */
export function rayRateToApyPercent(rate: BigInt): BigDecimal {
  return aprToApyPercent(toFloat(rate) / 1.0e27);
}

/**
 * Compound III: `getSupplyRate(getUtilization())` is a PER-SECOND growth rate
 * scaled 1e18 — not per block. rate/1e18 * secondsPerYear is the APR Compound's
 * own UI reports; compounding it per second gives the APY.
 */
export function perSecondRateToApyPercent(rate: BigInt): BigDecimal {
  return aprToApyPercent((toFloat(rate) / 1.0e18) * SECONDS_PER_YEAR);
}

/**
 * MetaMorpho: no rate field exists anywhere, so yield is only visible as
 * ERC-4626 share-price drift. Annualize the observed growth between two
 * samples of `totalAssets * 1e36 / totalSupply` (the 1e36 cancels in the
 * ratio). Mirrors SharePriceGrowthToAPY in apy.go.
 */
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
    // Share price should never fall. If it did, the vault took a loss or our
    // math is wrong; we publish no rate under either explanation.
    return BigDecimal.zero();
  }
  let periods = SECONDS_PER_YEAR / <f64>elapsedSeconds.toI64();
  return percent(Math.expm1(periods * Math.log1p(growth)) * 100.0);
}

/**
 * BigInt -> f64. Via the decimal string because a ray does not fit an i64:
 * f64 keeps ~16 significant digits, which is 10 orders of magnitude more
 * precision than any of these rates carry.
 */
export function toFloat(value: BigInt): f64 {
  return parseFloat(value.toString());
}

/**
 * Write the protocol-agnostic row. Every data source ends here, and nothing
 * else writes Venue — that is what makes the entity comparable across
 * protocols rather than three schemas wearing one name.
 */
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

/**
 * Stack the asset's own yield onto the venue's lending yield.
 *
 * The whole point of the split: Aave pays ~0.1% on wstETH because nobody
 * borrows it, while the wstETH itself accrues ~3% underneath. `totalApy` is
 * the number a router must compare on; `supplyApy` alone understates the
 * position by the entire staking rate.
 *
 * A feed that exists but is not `available` contributes nothing AND flips
 * `intrinsicApyAvailable` to false — an unmeasurable rate is not a zero rate.
 */
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

/**
 * Record that this venue is in this asset, so a later rate-feed sample can
 * refresh it. Append-only and deduplicated; a venue is never removed, because
 * a delisted venue keeps its row and simply goes inactive.
 */
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

/**
 * Re-apply an asset's intrinsic rate to every venue already indexed in it.
 * Called from the rate-feed sampler: rates move on their own schedule, not on
 * the lending protocols'.
 */
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
