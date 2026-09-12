import {
  Address,
  BigDecimal,
  BigInt,
  Bytes,
  dataSource,
  ethereum,
  log,
} from "@graphprotocol/graph-ts";
import { ChainlinkAggregator } from "../generated/RateFeeds/ChainlinkAggregator";
import { SsrOracle } from "../generated/RateFeeds/SsrOracle";
import { ERC20 } from "../generated/RateFeeds/ERC20";
import { RateFeed, RateFeedHourlySnapshot } from "../generated/schema";
import {
  ZERO,
  refreshVenuesForAsset,
  sharePriceGrowthToApyPercent,
  upsertVenue,
} from "./normalize";

// ---------------------------------------------------------------------------
// Intrinsic yield: what an asset earns with no protocol interaction at all.
//
// Every other data source in this subgraph indexes a LENDING market. A liquid
// staking token earns on its own — its exchange rate against the underlying
// just rises — and reporting that as zero is a mispricing, not a cosmetic gap:
// Aave pays ~0.1% on wstETH because nobody borrows it, so a 3.1% position looks
// like a 0.1% one and the keeper "improves" the user out of it.
//
// On Base none of these tokens can tell you their own rate. Every one is a
// bridged representation; stEthPerToken(), exchangeRate(), getRate(),
// getEETHByWeETH() and convertToAssets() were each called against
// https://mainnet.base.org on all six tokens below and every call REVERTED. The
// rate therefore comes from the oracle that publishes it on this chain, and is
// sampled over time exactly the way `Vault` samples MetaMorpho share price —
// same annualization, same minimum window, same refusal to publish a rate that
// fell.
// ---------------------------------------------------------------------------

// Rates are stored as a ray so two providers on different scales are directly
// comparable. Only the ratio of two samples is ever used, so the scale cancels.
// Chainlink exchange-rate feeds on Base report 18 decimals (verified with
// decimals() on each aggregator), so 1e9 lifts them to a ray.
const CHAINLINK_TO_RAY = BigInt.fromString("1000000000");

const KIND_CHAINLINK = "chainlink-exchange-rate";
const KIND_SKY_SSR = "sky-ssr";

export const PROTOCOL_HOLD = "hold";

// These feeds carry a 24h heartbeat, so a six-hour window can easily contain
// zero rounds and annualize to nonsense. A day is the shortest window that is
// guaranteed to span at least one publication.
const MIN_APY_WINDOW = BigInt.fromI32(24 * 3600);
// Drag the anchor forward weekly so the published rate is recent yield rather
// than an average over the feed's whole life.
const REANCHOR_WINDOW = BigInt.fromI32(7 * 24 * 3600);

const HOUR = 3600;

/** One token whose rate is published by `provider` on this chain. */
class RateSource {
  constructor(
    public token: Address,
    public symbol: string,
    public decimals: i32,
    public provider: Address,
    public kind: string
  ) {}
}

/**
 * The table, verified by eth_call on Base mainnet. Each provider's
 * description() is quoted, and each was sampled at four historical blocks over
 * 30 days to confirm the series is MONOTONIC — a market-price feed dips, an
 * exchange rate cannot, and only the latter is intrinsic yield.
 *
 * Deliberately absent:
 *
 *   ezETH  0x2416092f143378750bb29b79ed961ab195cceea5
 *     The only ETH-denominated ezETH feed on Base, 0x960BDD1d..., calls itself
 *     "ezETH / ETH" and is a market price, not an exchange rate: sampled over
 *     30 days it FELL (1.082079 -> 1.080979 -> 1.082490). Renzo publishes no
 *     rate provider on Base that could be verified. Reported unavailable rather
 *     than inventing a rate out of a price series.
 *
 *   syrupUSDC  0x660975730059246a68521a3e2fbd4740173100f5
 *     A bridged xERC20. exchangeRate(), getRate() and convertToAssets() all
 *     revert and Maple deploys no rate oracle on Base; its rate lives on
 *     Ethereum mainnet. Unavailable here.
 *
 *   USDS  0x820c137fa70c8691f0e44dc420a5e53c168921dc
 *     Not yield-bearing. USDS holders earn nothing; the savings rate is paid to
 *     sUSDS, which is listed below. Giving USDS an intrinsic APY would be the
 *     mirror image of the bug being fixed.
 */
function rateSources(): RateSource[] {
  // Not one of these tokens exists on Base Sepolia. Returning an empty table
  // makes the sampler a no-op there instead of six guaranteed reverts a block.
  if (dataSource.network() != "base") {
    return [];
  }
  return [
    // "wstETH-stETH Exchange Rate"
    new RateSource(
      Address.fromString("0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452"),
      "wstETH",
      18,
      Address.fromString("0xb88bac61a4ca37c43a3725912b1f472c9a5bc061"),
      KIND_CHAINLINK
    ),
    // "cbETH-ETH Exchange Rate"
    new RateSource(
      Address.fromString("0x2ae3f1ec7f1f5012cfeab0185bfc7aa3cf0dec22"),
      "cbETH",
      18,
      Address.fromString("0x868a501e68f3d1e89cfc0d22f6b22e8dabce5f04"),
      KIND_CHAINLINK
    ),
    // "weETH / eETH Exchange Rate" — eETH is 1:1 with ETH by construction.
    new RateSource(
      Address.fromString("0x04c0599ae5a44757c0af6f9ec3b93da8976c150a"),
      "weETH",
      18,
      Address.fromString("0x35e9d7001819ea3b39da906ae6b06a62cfe2c181"),
      KIND_CHAINLINK
    ),
    // "wrsETH-ETH Exchange Rate"
    new RateSource(
      Address.fromString("0xedfa23602d0ec14714057867a78d01e94176bea0"),
      "wrsETH",
      18,
      Address.fromString("0xe8dd07ccf5bc4922424140e44eb970f5950725ef"),
      KIND_CHAINLINK
    ),
    // Sky Savings Rate oracle: getConversionRate() is sUSDS' chi in ray, the
    // same accumulator sUSDS redeems against. getSUSDSData() returns
    // (ssr, chi, rho), which is how the address was confirmed to be sUSDS
    // accounting and not some other rate.
    new RateSource(
      Address.fromString("0x5875eee11cf8398102fdad704c9e96607675467a"),
      "sUSDS",
      18,
      Address.fromString("0x65d946e533748a998b1f0e430803e39a6388f7a1"),
      KIND_SKY_SSR
    ),
  ];
}

/**
 * Polling entry point. Sampling on a block schedule rather than on events is
 * forced by the data: a Chainlink proxy emits nothing, the aggregator behind it
 * does, and aggregators get swapped out from under the proxy.
 */
export function handleBlock(block: ethereum.Block): void {
  let sources = rateSources();
  for (let i = 0; i < sources.length; i++) {
    sample(sources[i], block);
  }
}

function sample(src: RateSource, block: ethereum.Block): void {
  let id = src.token.toHexString();
  let feed = RateFeed.load(id);
  if (feed == null) {
    feed = new RateFeed(id);
    feed.token = changetype<Bytes>(src.token);
    feed.symbol = src.symbol;
    feed.decimals = src.decimals;
    feed.rateScaled = ZERO;
    feed.anchorRateScaled = ZERO;
    feed.anchorTimestamp = ZERO;
    feed.intrinsicApy = BigDecimal.zero();
    feed.available = false;
    feed.unavailableReason = "no-anchor";
    feed.totalSupply = ZERO;
  }
  feed.provider = changetype<Bytes>(src.provider);
  feed.providerKind = src.kind;
  feed.lastUpdateBlock = block.number;
  feed.lastUpdateTimestamp = block.timestamp;

  let supply = ERC20.bind(src.token).try_totalSupply();
  if (!supply.reverted) {
    feed.totalSupply = supply.value;
  }

  let rate = readRate(src);
  if (rate.le(ZERO)) {
    // A rate that cannot be read is NOT zero. Keep the last good rate and the
    // last good APY off the wire; mark the feed unavailable and say why.
    markUnavailable(feed, "reverted");
    log.warning("rate feed unreadable, intrinsic apy withheld: {} via {}", [
      src.symbol,
      src.provider.toHexString(),
    ]);
    finish(feed, src, block);
    return;
  }

  let previous = feed.rateScaled;
  feed.rateScaled = rate;
  if (previous.gt(ZERO) && rate.lt(previous)) {
    // An exchange rate must not fall. A slashing event or a broken oracle look
    // identical from here, and we route on neither.
    log.warning("rate feed fell, intrinsic apy withheld: {} {} -> {}", [
      src.symbol,
      previous.toString(),
      rate.toString(),
    ]);
  }
  updateApy(feed, block.timestamp);
  finish(feed, src, block);
}

/** Anchor-and-annualize, the same shape `Vault.updateApy` uses for Morpho. */
function updateApy(feed: RateFeed, timestamp: BigInt): void {
  if (feed.anchorRateScaled.le(ZERO)) {
    feed.anchorRateScaled = feed.rateScaled;
    feed.anchorTimestamp = timestamp;
    markUnavailable(feed, "no-anchor");
    return;
  }

  let elapsed = timestamp.minus(feed.anchorTimestamp);
  if (elapsed.lt(MIN_APY_WINDOW)) {
    // Annualizing a few minutes of drift produces nonsense, so nothing is
    // published — and "nothing" is explicitly not 0%.
    markUnavailable(feed, "window-too-short");
    return;
  }
  if (feed.rateScaled.lt(feed.anchorRateScaled)) {
    markUnavailable(feed, "rate-fell");
    return;
  }

  let apy = sharePriceGrowthToApyPercent(
    feed.anchorRateScaled,
    feed.rateScaled,
    elapsed
  );
  if (apy.gt(BigDecimal.zero())) {
    feed.intrinsicApy = apy;
    feed.available = true;
    feed.unavailableReason = null;
  } else {
    // Flat over a full day means a stale feed, not a zero-yield asset.
    markUnavailable(feed, "no-growth");
  }

  if (elapsed.ge(REANCHOR_WINDOW)) {
    feed.anchorRateScaled = feed.rateScaled;
    feed.anchorTimestamp = timestamp;
  }
}

function markUnavailable(feed: RateFeed, reason: string): void {
  feed.available = false;
  feed.unavailableReason = reason;
  feed.intrinsicApy = BigDecimal.zero();
}

/**
 * Persist, then publish the two things that depend on the rate:
 *
 *   1. the `hold` venue — holding the token IS the position, there is no
 *      protocol to deposit into, so the venue's pool key is the token itself
 *      and its TVL is the token's total supply on this chain;
 *   2. every lending venue in the same asset, so an Aave wstETH reserve that
 *      has not been touched in two days still reports the current staking rate.
 */
function finish(feed: RateFeed, src: RateSource, block: ethereum.Block): void {
  feed.save();
  writeSnapshot(feed, block);
  upsertVenue(
    PROTOCOL_HOLD,
    src.token,
    src.symbol,
    src.token,
    src.symbol,
    src.decimals,
    BigDecimal.zero(), // no lending leg: nobody is borrowing from your wallet
    feed.totalSupply,
    null,
    feed.available,
    block
  );
  refreshVenuesForAsset(src.token);
}

function writeSnapshot(feed: RateFeed, block: ethereum.Block): void {
  if (feed.rateScaled.le(ZERO)) {
    return;
  }
  let hourIndex = block.timestamp.toI32() / HOUR;
  let id = feed.id + "-" + hourIndex.toString();
  let snap = RateFeedHourlySnapshot.load(id);
  if (snap == null) {
    snap = new RateFeedHourlySnapshot(id);
    snap.feed = feed.id;
    snap.hourIndex = hourIndex;
    snap.hourStartTimestamp = BigInt.fromI32(hourIndex).times(
      BigInt.fromI32(HOUR)
    );
    snap.sampleCount = 0;
  }
  snap.timestamp = block.timestamp;
  snap.block = block.number;
  snap.rateScaled = feed.rateScaled;
  snap.sampleCount = snap.sampleCount + 1;
  snap.save();
}

/** Read and normalize to a ray. Zero means "could not read", never "no yield". */
function readRate(src: RateSource): BigInt {
  if (src.kind == KIND_SKY_SSR) {
    let chi = SsrOracle.bind(src.provider).try_getConversionRate();
    if (chi.reverted || chi.value.le(ZERO)) {
      return ZERO;
    }
    return chi.value; // already a ray
  }
  let answer = ChainlinkAggregator.bind(src.provider).try_latestAnswer();
  if (answer.reverted || answer.value.le(ZERO)) {
    return ZERO;
  }
  return answer.value.times(CHAINLINK_TO_RAY);
}
