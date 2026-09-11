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
import { Vault, VaultDeposit, VaultHourlySnapshot, VaultWithdraw } from "../generated/schema";

export const ZERO = BigInt.zero();
export const ONE = BigInt.fromI32(1);
const HOUR = 3600;

// Scale-free integer share price: totalAssets * 1e36 / totalSupply.
// 1e36 (not 1e18) because MetaMorpho shares carry a virtual decimals offset,
// so the raw ratio can be ~1e-12 and 1e18 would leave only ~6 significant
// digits — not enough to see an hour of yield.
const SHARE_PRICE_SCALE = BigInt.fromI32(10).pow(36);

/**
 * Re-read totalAssets/totalSupply from the vault and refresh the Vault row and
 * the current hourly bucket. Every handler funnels through here so there is one
 * place that knows how share price is computed.
 */
export function syncVault(vaultAddress: Address, event: ethereum.Event): Vault | null {
  let vault = Vault.load(vaultAddress.toHexString());
  if (vault == null) {
    return null;
  }

  let contract = MetaMorpho.bind(vaultAddress);

  // A paused or broken vault must not kill indexing for every other vault.
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
  vault.save();

  writeSnapshot(vault, event);
  return vault;
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

/**
 * Hourly bucket, last-write-wins within the hour. A bucket only exists if the
 * vault was touched during that hour; `timestamp` is the real sample time so a
 * consumer never has to assume the spacing it asked for.
 */
function writeSnapshot(vault: Vault, event: ethereum.Event): void {
  let hourIndex = event.block.timestamp.toI32() / HOUR;
  let id = vault.id + "-" + hourIndex.toString();

  let snap = VaultHourlySnapshot.load(id);
  if (snap == null) {
    snap = new VaultHourlySnapshot(id);
    snap.vault = vault.id;
    snap.hourIndex = hourIndex;
    snap.hourStartTimestamp = BigInt.fromI32(hourIndex).times(BigInt.fromI32(HOUR));
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

  vault.cumulativeDeposited = vault.cumulativeDeposited.plus(event.params.assets);
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

  vault.cumulativeWithdrawn = vault.cumulativeWithdrawn.plus(event.params.assets);
  vault.withdrawCount = vault.withdrawCount.plus(ONE);
  vault.save();
}

// Interest accrual is where yield actually appears; it fires on every vault
// interaction, which is what keeps the hourly series populated between
// deposits and withdrawals.
export function handleAccrueInterest(event: AccrueInterest): void {
  syncVault(event.address, event);
}

export function handleUpdateLastTotalAssets(event: UpdateLastTotalAssets): void {
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
