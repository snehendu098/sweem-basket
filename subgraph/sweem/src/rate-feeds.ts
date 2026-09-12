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

const CHAINLINK_TO_RAY = BigInt.fromString("1000000000");

const KIND_CHAINLINK = "chainlink-exchange-rate";
const KIND_SKY_SSR = "sky-ssr";

export const PROTOCOL_HOLD = "hold";

const MIN_APY_WINDOW = BigInt.fromI32(24 * 3600);
const REANCHOR_WINDOW = BigInt.fromI32(7 * 24 * 3600);

const HOUR = 3600;

class RateSource {
  constructor(
    public token: Address,
    public symbol: string,
    public decimals: i32,
    public provider: Address,
    public kind: string
  ) {}
}

function rateSources(): RateSource[] {
  if (dataSource.network() != "base") {
    return [];
  }
  return [
    new RateSource(
      Address.fromString("0xc1cba3fcea344f92d9239c08c0568f6f2f0ee452"),
      "wstETH",
      18,
      Address.fromString("0xb88bac61a4ca37c43a3725912b1f472c9a5bc061"),
      KIND_CHAINLINK
    ),
    new RateSource(
      Address.fromString("0x2ae3f1ec7f1f5012cfeab0185bfc7aa3cf0dec22"),
      "cbETH",
      18,
      Address.fromString("0x868a501e68f3d1e89cfc0d22f6b22e8dabce5f04"),
      KIND_CHAINLINK
    ),
    new RateSource(
      Address.fromString("0x04c0599ae5a44757c0af6f9ec3b93da8976c150a"),
      "weETH",
      18,
      Address.fromString("0x35e9d7001819ea3b39da906ae6b06a62cfe2c181"),
      KIND_CHAINLINK
    ),
    new RateSource(
      Address.fromString("0xedfa23602d0ec14714057867a78d01e94176bea0"),
      "wrsETH",
      18,
      Address.fromString("0xe8dd07ccf5bc4922424140e44eb970f5950725ef"),
      KIND_CHAINLINK
    ),
    new RateSource(
      Address.fromString("0x5875eee11cf8398102fdad704c9e96607675467a"),
      "sUSDS",
      18,
      Address.fromString("0x65d946e533748a998b1f0e430803e39a6388f7a1"),
      KIND_SKY_SSR
    ),
  ];
}

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
    log.warning("rate feed fell, intrinsic apy withheld: {} {} -> {}", [
      src.symbol,
      previous.toString(),
      rate.toString(),
    ]);
  }
  updateApy(feed, block.timestamp);
  finish(feed, src, block);
}

function updateApy(feed: RateFeed, timestamp: BigInt): void {
  if (feed.anchorRateScaled.le(ZERO)) {
    feed.anchorRateScaled = feed.rateScaled;
    feed.anchorTimestamp = timestamp;
    markUnavailable(feed, "no-anchor");
    return;
  }

  let elapsed = timestamp.minus(feed.anchorTimestamp);
  if (elapsed.lt(MIN_APY_WINDOW)) {
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
    BigDecimal.zero(),
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

function readRate(src: RateSource): BigInt {
  if (src.kind == KIND_SKY_SSR) {
    let chi = SsrOracle.bind(src.provider).try_getConversionRate();
    if (chi.reverted || chi.value.le(ZERO)) {
      return ZERO;
    }
    return chi.value;
  }
  let answer = ChainlinkAggregator.bind(src.provider).try_latestAnswer();
  if (answer.reverted || answer.value.le(ZERO)) {
    return ZERO;
  }
  return answer.value.times(CHAINLINK_TO_RAY);
}
