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

const VERSION_ADOPTED: i32 = 0;

function knownVaults(): string[] {
  if (dataSource.network() == "base") {
    return [
      "0xeE8F4eC5672F09119b96Ab6fB59C27E1b7e44b61", // gtUSDCp
      "0x7BfA7C4f149E7415b73bdeDfe609237e29CBF34A", // sparkUSDC
      "0xbeeF010f9cb27031ad51e3333f9aF9C6B1228183", // steakUSDC
      "0xBeEf2d50B428675a1921bC6bBF4bfb9D8cF1461A", // grove-bbqUSDC
      "0x2C6D169782bF18Cc634D076Fe639092227B82fdA", // frUSDC
      "0xBEEFE94c8aD530842bfE7d8B397938fFc1cb83b2", // steakUSDC Prime
      "0x1401d1271C47648AC70cBcdfA3776D4A87CE006B", // pUSDC
      "0xc1256Ae5FF1cf2719D4937adb3bbCCab2E00A2Ca", // mwUSDC
      "0xa0E430870c4604CcfC7B38Ca7845B1FF653D0ff1", // mwETH
    ];
  }
  return [];
}

export function handleFactoryInit(block: ethereum.Block): void {
  let vaults = knownVaults();
  for (let i = 0; i < vaults.length; i++) {
    createVault(Address.fromString(vaults[i]), VERSION_ADOPTED, block);
  }
}

export function handleCreateMetaMorpho(event: CreateMetaMorpho): void {
  createVault(event.params.metaMorpho, 1, event.block);
}

export function handleCreateMetaMorphoV1_1(event: CreateMetaMorpho): void {
  createVault(event.params.metaMorpho, 11, event.block);
}

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
