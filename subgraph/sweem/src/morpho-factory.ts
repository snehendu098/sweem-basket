import {
  Address,
  BigDecimal,
  Bytes,
  dataSource,
  ethereum,
} from "@graphprotocol/graph-ts";
import { CreateMetaMorpho } from "../generated/MetaMorphoFactory/MetaMorphoFactory";
import { ERC20 } from "../generated/MetaMorphoFactory/ERC20";
import { MetaMorpho as MetaMorphoTemplate } from "../generated/templates";
import { MetaMorpho } from "../generated/templates/MetaMorpho/MetaMorpho";
import { Vault } from "../generated/schema";
import { ZERO, upsertVenue } from "./normalize";

export const PROTOCOL = "morpho-blue";

// Version recorded for a vault adopted by address rather than observed being
// created, so we never claim to know which factory minted it.
const VERSION_ADOPTED: i32 = 0;

/**
 * MetaMorpho vaults that exist before this deployment's start block.
 *
 * A vault is normally discovered from `CreateMetaMorpho`, which spawns a
 * template. On Base mainnet those events fired between blocks ~13.9M and ~23.9M
 * and the template would then replay each vault's entire life — for a $420M
 * vault that is millions of events, each with two eth_calls. Starting the
 * factory near chain head makes the sync tractable but discovers nothing, so the
 * vaults that already exist are adopted by address instead.
 *
 * Every address below was verified on-chain: `MORPHO()` returns Morpho Blue on
 * Base (0xBBBB…EFFCb), `asset()` and `symbol()` match, and `totalAssets()` is
 * non-zero. The factories stay in the manifest so vaults created from the start
 * block onward are still picked up the normal way.
 *
 * Base Sepolia returns an empty list: it starts at the factory's real deploy
 * block, so ordinary discovery works there and nothing needs adopting.
 */
function knownVaults(): string[] {
  if (dataSource.network() == "base") {
    return [
      "0xeE8F4eC5672F09119b96Ab6fB59C27E1b7e44b61", // gtUSDCp       Gauntlet USDC Prime
      "0x7BfA7C4f149E7415b73bdeDfe609237e29CBF34A", // sparkUSDC     Spark USDC Vault
      "0xbeeF010f9cb27031ad51e3333f9aF9C6B1228183", // steakUSDC     Steakhouse USDC
      "0xBeEf2d50B428675a1921bC6bBF4bfb9D8cF1461A", // grove-bbqUSDC Grove x Steakhouse USDC High Yield
      "0x2C6D169782bF18Cc634D076Fe639092227B82fdA", // frUSDC        Froge's USDC
      "0xBEEFE94c8aD530842bfE7d8B397938fFc1cb83b2", // steakUSDC     Steakhouse Prime USDC
      "0x1401d1271C47648AC70cBcdfA3776D4A87CE006B", // pUSDC         Pangolins USDC
      "0xc1256Ae5FF1cf2719D4937adb3bbCCab2E00A2Ca", // mwUSDC        Moonwell Flagship USDC
      "0xa0E430870c4604CcfC7B38Ca7845B1FF653D0ff1", // mwETH         Moonwell Flagship ETH
    ];
  }
  return [];
}

/** Runs once at the factory's start block: adopt the pre-existing vaults. */
export function handleFactoryInit(block: ethereum.Block): void {
  let vaults = knownVaults();
  for (let i = 0; i < vaults.length; i++) {
    createVault(Address.fromString(vaults[i]), VERSION_ADOPTED, block);
  }
}

export function handleCreateMetaMorpho(event: CreateMetaMorpho): void {
  createVault(event.params.metaMorpho, 1, event.block);
}

// v1.1 emits the identical CreateMetaMorpho signature, so the same decoded
// event type serves both factories; only the recorded version differs.
export function handleCreateMetaMorphoV1_1(event: CreateMetaMorpho): void {
  createVault(event.params.metaMorpho, 11, event.block);
}

/**
 * Build the Vault row entirely from on-chain reads and start indexing it.
 *
 * Reading rather than trusting the event params is what lets the same function
 * serve both discovery paths. `asset()` doubles as the liveness check: if it
 * reverts the address is not a deployed ERC-4626 at this block, and writing a
 * Venue for it would publish a venue that does not exist.
 */
function createVault(address: Address, version: i32, block: ethereum.Block): void {
  let id = address.toHexString();
  if (Vault.load(id) != null) {
    return;
  }

  let contract = MetaMorpho.bind(address);
  let assetCall = contract.try_asset();
  if (assetCall.reverted) {
    return;
  }
  let asset = assetCall.value;

  let vault = new Vault(id);
  vault.asset = changetype<Bytes>(asset);
  vault.factory =
    version == VERSION_ADOPTED ? Bytes.empty() : changetype<Bytes>(dataSource.address());
  vault.version = version;

  let name = contract.try_name();
  vault.name = name.reverted ? "" : name.value;
  let symbol = contract.try_symbol();
  vault.symbol = symbol.reverted ? "" : symbol.value;

  let token = ERC20.bind(asset);
  let assetSymbol = token.try_symbol();
  vault.assetSymbol = assetSymbol.reverted ? "" : assetSymbol.value;
  let assetDecimals = token.try_decimals();
  vault.assetDecimals = assetDecimals.reverted ? 18 : assetDecimals.value;

  let decimals = contract.try_decimals();
  vault.decimals = decimals.reverted ? 18 : decimals.value;
  let curator = contract.try_curator();
  vault.curator = curator.reverted ? null : changetype<Bytes>(curator.value);
  let owner = contract.try_owner();
  vault.owner = owner.reverted ? null : changetype<Bytes>(owner.value);
  let fee = contract.try_fee();
  vault.fee = fee.reverted ? null : fee.value;

  // Share price only becomes meaningful once the vault has supply, and an APY
  // only after the first window elapses, so seed at zero either way — a newly
  // created vault is empty and an adopted one gets its first sample below.
  vault.totalAssets = ZERO;
  vault.totalSupply = ZERO;
  vault.sharePrice = BigDecimal.zero();
  vault.sharePriceScaled = ZERO;
  vault.anchorSharePriceScaled = ZERO;
  vault.anchorTimestamp = ZERO;
  vault.supplyApy = BigDecimal.zero();
  vault.cumulativeDeposited = ZERO;
  vault.cumulativeWithdrawn = ZERO;
  vault.depositCount = ZERO;
  vault.withdrawCount = ZERO;
  vault.createdAtBlock = block.number;
  vault.createdAtTimestamp = block.timestamp;
  vault.lastUpdateBlock = block.number;
  vault.lastUpdateTimestamp = block.timestamp;
  vault.save();

  // Publish the venue immediately, inactive and rate-less. A router that sees
  // an empty row learns the vault exists; a missing row looks like an outage.
  upsertVenue(
    PROTOCOL,
    address,
    vault.symbol,
    asset,
    vault.assetSymbol,
    vault.assetDecimals,
    BigDecimal.zero(),
    ZERO,
    null,
    false,
    block
  );

  MetaMorphoTemplate.create(address);
}
