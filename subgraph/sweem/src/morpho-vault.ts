import {
  Address,
  BigDecimal,
  BigInt,
  Bytes,
  ethereum,
} from "@graphprotocol/graph-ts";
import {
  AccrueInterest,
  Deposit,
  MetaMorpho,
  SetCurator,
  SetFee,
  UpdateLastTotalAssets,
  Withdraw,
} from "../generated/templates/MetaMorpho/MetaMorpho";
import {
  Vault,
  VaultDeposit,
  VaultHourlySnapshot,
  VaultWithdraw,
} from "../generated/schema";
import {
  ZERO,
  sharePriceGrowthToApyPercent,
  upsertVenue,
} from "./normalize";
import { PROTOCOL } from "./morpho-factory";

const ONE = BigInt.fromI32(1);
const HOUR = 3600;

// 1e36, not 1e18: MetaMorpho's 12-decimal virtual offset makes the raw ratio
// ~1e-12, and 1e18 would round an hour of yield to nothing.
const SHARE_PRICE_SCALE = BigInt.fromI32(10).pow(36);

const MIN_APY_WINDOW = BigInt.fromI32(6 * 3600);
const REANCHOR_WINDOW = BigInt.fromI32(24 * 3600);

function syncVault(
  vaultAddress: Address,
  event: ethereum.Event
): Vault | null {
  let vault = Vault.load(vaultAddress.toHexString());
  if (vault == null) {
    return null;
  }

  let contract = MetaMorpho.bind(vaultAddress);

  let assets = contract.try_totalAssets();
  let supply = contract.try_totalSupply();
  if (assets.reverted || supply.reverted) {
    return vault;
  }

  vault.totalAssets = assets.value;
  vault.totalSupply = supply.value;
  vault.sharePrice = sharePriceDecimal(assets.value, supply.value);
  vault.sharePriceScaled = sharePriceInt(assets.value, supply.value);
  vault.lastUpdateBlock = event.block.number;
  vault.lastUpdateTimestamp = event.block.timestamp;

  updateApy(vault, event.block.timestamp);
  vault.save();

  writeSnapshot(vault, event);
  publishVenue(vault, event);
  return vault;
}

function updateApy(vault: Vault, timestamp: BigInt): void {
  let price = vault.sharePriceScaled;
  if (price.le(ZERO)) {
    return;
  }
  if (vault.anchorSharePriceScaled.le(ZERO)) {
    vault.anchorSharePriceScaled = price;
    vault.anchorTimestamp = timestamp;
    return;
  }

  let elapsed = timestamp.minus(vault.anchorTimestamp);
  if (elapsed.ge(MIN_APY_WINDOW)) {
    vault.supplyApy = sharePriceGrowthToApyPercent(
      vault.anchorSharePriceScaled,
      price,
      elapsed
    );
  }
  if (elapsed.ge(REANCHOR_WINDOW)) {
    vault.anchorSharePriceScaled = price;
    vault.anchorTimestamp = timestamp;
  }
}

function publishVenue(vault: Vault, event: ethereum.Event): void {
  upsertVenue(
    PROTOCOL,
    Address.fromString(vault.id),
    vault.symbol,
    Address.fromBytes(vault.asset),
    vault.assetSymbol,
    vault.assetDecimals,
    vault.supplyApy,
    vault.totalAssets,
    null,
    vault.totalSupply.gt(ZERO),
    event.block
  );
}

function sharePriceDecimal(assets: BigInt, supply: BigInt): BigDecimal {
  if (supply.equals(ZERO)) {
    return BigDecimal.zero();
  }
  return assets.toBigDecimal().div(supply.toBigDecimal());
}

function sharePriceInt(assets: BigInt, supply: BigInt): BigInt {
  if (supply.equals(ZERO)) {
    return ZERO;
  }
  return assets.times(SHARE_PRICE_SCALE).div(supply);
}

function writeSnapshot(vault: Vault, event: ethereum.Event): void {
  let hourIndex = event.block.timestamp.toI32() / HOUR;
  let id = vault.id + "-" + hourIndex.toString();

  let snap = VaultHourlySnapshot.load(id);
  if (snap == null) {
    snap = new VaultHourlySnapshot(id);
    snap.vault = vault.id;
    snap.hourIndex = hourIndex;
    snap.hourStartTimestamp = BigInt.fromI32(hourIndex).times(
      BigInt.fromI32(HOUR)
    );
    snap.sampleCount = 0;
  }

  snap.timestamp = event.block.timestamp;
  snap.block = event.block.number;
  snap.totalAssets = vault.totalAssets;
  snap.totalSupply = vault.totalSupply;
  snap.sharePrice = vault.sharePrice;
  snap.sharePriceScaled = vault.sharePriceScaled;
  snap.sampleCount = snap.sampleCount + 1;
  snap.save();
}

export function handleDeposit(event: Deposit): void {
  let vault = syncVault(event.address, event);
  if (vault == null) {
    return;
  }

  let entry = new VaultDeposit(eventId(event));
  entry.vault = vault.id;
  entry.sender = event.params.sender;
  entry.owner = event.params.owner;
  entry.assets = event.params.assets;
  entry.shares = event.params.shares;
  entry.block = event.block.number;
  entry.timestamp = event.block.timestamp;
  entry.txHash = event.transaction.hash;
  entry.save();

  vault.cumulativeDeposited = vault.cumulativeDeposited.plus(
    event.params.assets
  );
  vault.depositCount = vault.depositCount.plus(ONE);
  vault.save();
}

export function handleWithdraw(event: Withdraw): void {
  let vault = syncVault(event.address, event);
  if (vault == null) {
    return;
  }

  let entry = new VaultWithdraw(eventId(event));
  entry.vault = vault.id;
  entry.sender = event.params.sender;
  entry.receiver = event.params.receiver;
  entry.owner = event.params.owner;
  entry.assets = event.params.assets;
  entry.shares = event.params.shares;
  entry.block = event.block.number;
  entry.timestamp = event.block.timestamp;
  entry.txHash = event.transaction.hash;
  entry.save();

  vault.cumulativeWithdrawn = vault.cumulativeWithdrawn.plus(
    event.params.assets
  );
  vault.withdrawCount = vault.withdrawCount.plus(ONE);
  vault.save();
}

export function handleAccrueInterest(event: AccrueInterest): void {
  syncVault(event.address, event);
}

export function handleUpdateLastTotalAssets(
  event: UpdateLastTotalAssets
): void {
  syncVault(event.address, event);
}

export function handleSetFee(event: SetFee): void {
  let vault = Vault.load(event.address.toHexString());
  if (vault == null) {
    return;
  }
  vault.fee = event.params.newFee;
  vault.lastUpdateBlock = event.block.number;
  vault.lastUpdateTimestamp = event.block.timestamp;
  vault.save();
}

export function handleSetCurator(event: SetCurator): void {
  let vault = Vault.load(event.address.toHexString());
  if (vault == null) {
    return;
  }
  vault.curator = changetype<Bytes>(event.params.newCurator);
  vault.lastUpdateBlock = event.block.number;
  vault.lastUpdateTimestamp = event.block.timestamp;
  vault.save();
}

function eventId(event: ethereum.Event): string {
  return event.transaction.hash.toHexString() + "-" + event.logIndex.toString();
}
