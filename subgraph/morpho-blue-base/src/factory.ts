import { Address, BigDecimal, BigInt, Bytes } from "@graphprotocol/graph-ts";
import { CreateMetaMorpho } from "../generated/MetaMorphoFactory/MetaMorphoFactory";
import { ERC20 } from "../generated/MetaMorphoFactory/ERC20";
import { MetaMorpho as MetaMorphoTemplate } from "../generated/templates";
import { MetaMorpho } from "../generated/templates/MetaMorpho/MetaMorpho";
import { Vault } from "../generated/schema";

const ZERO = BigInt.zero();

export function handleCreateMetaMorpho(event: CreateMetaMorpho): void {
  createVault(event, 1);
}

export function handleCreateMetaMorphoV1_1(event: CreateMetaMorpho): void {
  createVault(event, 11);
}

function createVault(event: CreateMetaMorpho, version: i32): void {
  let address = event.params.metaMorpho;
  let id = address.toHexString();
  if (Vault.load(id) != null) {
    return;
  }

  let vault = new Vault(id);
  vault.name = event.params.name;
  vault.symbol = event.params.symbol;
  vault.asset = changetype<Bytes>(event.params.asset);
  vault.factory = changetype<Bytes>(event.address);
  vault.version = version;

  let token = ERC20.bind(event.params.asset);
  let assetSymbol = token.try_symbol();
  vault.assetSymbol = assetSymbol.reverted ? "" : assetSymbol.value;
  let assetDecimals = token.try_decimals();
  vault.assetDecimals = assetDecimals.reverted ? 18 : assetDecimals.value;

  let contract = MetaMorpho.bind(address);
  let decimals = contract.try_decimals();
  vault.decimals = decimals.reverted ? 18 : decimals.value;
  let curator = contract.try_curator();
  vault.curator = curator.reverted ? null : changetype<Bytes>(curator.value);
  let owner = contract.try_owner();
  vault.owner = owner.reverted ? null : changetype<Bytes>(owner.value);
  let fee = contract.try_fee();
  vault.fee = fee.reverted ? null : fee.value;

  // A freshly created vault is empty; share price only becomes meaningful once
  // it has supply, so seed at zero and let the first vault event fill it in.
  vault.totalAssets = ZERO;
  vault.totalSupply = ZERO;
  vault.sharePrice = BigDecimal.zero();
  vault.sharePriceScaled = ZERO;
  vault.cumulativeDeposited = ZERO;
  vault.cumulativeWithdrawn = ZERO;
  vault.depositCount = ZERO;
  vault.withdrawCount = ZERO;
  vault.createdAtBlock = event.block.number;
  vault.createdAtTimestamp = event.block.timestamp;
  vault.lastUpdateBlock = event.block.number;
  vault.lastUpdateTimestamp = event.block.timestamp;
  vault.save();

  MetaMorphoTemplate.create(address);
}
