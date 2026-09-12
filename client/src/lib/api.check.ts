/**
 * Self-check for the rules that must not silently regress:
 *   - 207 Multi-Status is a result, not a thrown error
 *   - both services' error envelopes are unwrapped to a real message
 *   - a null price renders as "value unknown", never a fabricated dollar
 *   - an uncomputable basket APY is null, never 0
 *   - an even split always sums to exactly 10000 bps
 *   - a chain label only ever matches its own network
 *
 * Run: bun run src/lib/api.check.ts
 */
import assert from "node:assert/strict";
import {
  ApiError,
  DEFAULT_CHAIN_ID,
  isSwappable,
  swappableAssets,
  activeChain,
  basescanTx,
  blendApy,
  displayAsset,
  sameChain,
  evenSplit,
  readBalances,
  setActiveChainId,
  fmtUsd,
  fmtUsdOrUnknown,
  market,
  publicBaskets,
  walletFetch,
} from "./api";
import { ownership } from "./types";
import type { AssetSummary, BasketSummary } from "./types";

type Stub = { status: number; body: unknown };
const realFetch = globalThis.fetch;
function stub(s: Stub) {
  globalThis.fetch = (async () =>
    new Response(JSON.stringify(s.body), { status: s.status })) as typeof fetch;
}

async function main() {
  // 207: partial success must come back to the caller intact.
  stub({
    status: 207,
    body: { failed_legs: 1, pending_legs: 1, legs: [{ status: "failed" }] },
  });
  const res = await walletFetch<{ failed_legs: number }>("/v1/x", "tok");
  assert.equal(res.status, 207);
  assert.equal(res.data.failed_legs, 1);

  // wallet service errors are {"error": "msg"}
  stub({ status: 412, body: { error: "cannot execute: wallet delegation is missing" } });
  await assert.rejects(
    () => walletFetch("/v1/x", "tok"),
    (e: unknown) =>
      e instanceof ApiError &&
      e.status === 412 &&
      e.message.includes("delegation is missing"),
  );

  // market-data errors are {"error":{"code","message"}} and successes are {"data":…}
  stub({ status: 404, body: { error: { code: "no_venue", message: "no venue matches asset=DOGE" } } });
  await assert.rejects(
    () => market.assets(),
    (e: unknown) => e instanceof ApiError && e.message.includes("DOGE"),
  );

  stub({ status: 200, body: { data: { assets: [{ asset: "USDC" }], count: 1 } } });
  const assets = await market.assets();
  assert.equal(assets.count, 1);

  // Prices: null is unknown, and unknown never becomes a number.
  assert.equal(fmtUsdOrUnknown(null), "value unknown");
  assert.equal(fmtUsdOrUnknown(undefined), "value unknown");
  assert.equal(fmtUsdOrUnknown(0), "$0.00");
  assert.equal(fmtUsd(1234.5), "$1,234.50");

  // Basket APY: weighted over the per-asset best venue, and null — never 0 —
  // when any weighted asset has no venue at all.
  const summaries = [
    { asset: "USDC", best_apy: 10 },
    { asset: "DAI", best_apy: 5 },
  ] as AssetSummary[];
  assert.equal(
    blendApy(
      [
        { asset: "USDC", weight_bps: 6000 },
        { asset: "DAI", weight_bps: 4000 },
      ],
      summaries,
    ),
    8,
  );
  assert.equal(blendApy([{ asset: "PYUSD", weight_bps: 10000 }], summaries), null);
  assert.equal(blendApy([], summaries), null);
  assert.equal(blendApy([{ asset: "USDC", weight_bps: 10000 }], null), null);

  // Even split: the backend rejects anything that is not exactly 10000, so the
  // rounding remainder must land somewhere. 3 assets is 3334/3333/3333.
  for (let n = 1; n <= 8; n++) {
    const assets = Array.from({ length: n }, (_, i) => `A${i}`);
    const split = evenSplit(assets);
    assert.equal(split.length, n);
    assert.equal(
      split.reduce((s, w) => s + w.weight_bps, 0),
      10000,
      `even split of ${n} assets must sum to 10000`,
    );
    assert.ok(split.every((w) => w.weight_bps > 0));
    // Nobody's slice is more than 1 bp off anybody else's.
    const min = Math.min(...split.map((w) => w.weight_bps));
    const max = Math.max(...split.map((w) => w.weight_bps));
    assert.ok(max - min <= 1);
    assert.deepEqual(
      split.map((w) => w.asset),
      assets,
    );
  }
  assert.deepEqual(evenSplit([]), []);
  assert.deepEqual(
    evenSplit(["USDC", "WETH", "DAI"]).map((w) => w.weight_bps),
    [3334, 3333, 3333],
  );
  // Percent readout uses the same rule, so it reads 34/33/33 and sums to 100.
  assert.deepEqual(
    evenSplit(["USDC", "WETH", "DAI"], 100).map((w) => w.weight_bps),
    [34, 33, 33],
  );

  // Display mapping is display-only: the wire symbol is never rewritten.
  assert.equal(displayAsset("WETH"), "ETH");
  assert.equal(displayAsset("USDC"), "USDC");
  assert.equal(evenSplit(["WETH"])[0].asset, "WETH");

  // Public browsing needs no token, and the list unwraps either envelope shape.
  stub({ status: 200, body: { data: [{ id: "a" }] } });
  assert.equal((await publicBaskets()).length, 1);
  stub({ status: 200, body: { data: { baskets: [{ id: "a" }, { id: "b" }] } } });
  assert.equal((await publicBaskets()).length, 2);

  // The active chain is what every chain-dependent read follows: the label the
  // API is asked for and the explorer a hash links to.
  setActiveChainId(8453);
  assert.equal(activeChain().label, "base");
  assert.equal(activeChain().testnet, false);
  assert.ok(basescanTx("0xabc").startsWith("https://basescan.org/tx/"));
  setActiveChainId(84532);
  assert.equal(activeChain().label, "base-sepolia");
  assert.equal(activeChain().testnet, true);
  assert.ok(basescanTx("0xabc").startsWith("https://sepolia.basescan.org/tx/"));
  setActiveChainId(DEFAULT_CHAIN_ID);

  // Direct balance read: right calldata, right decimals, and a failed leg is
  // null — never 0, which would render as an empty wallet.
  const calls: string[] = [];
  globalThis.fetch = (async (_url: string, init: RequestInit) => {
    const req = JSON.parse(String(init.body)) as {
      method: string;
      params: unknown[];
    };
    calls.push(req.method);
    if (req.method === "eth_getBalance") {
      // 1.5 ETH in wei.
      return new Response(
        JSON.stringify({ result: "0x14d1120d7b160000" }),
        { status: 200 },
      );
    }
    const call = req.params[0] as { data: string };
    // balanceOf(address): selector plus the address padded to 32 bytes.
    assert.equal(
      call.data,
      "0x70a08231000000000000000000000000000000000000000000000000000000000000dead",
    );
    return new Response(JSON.stringify({ result: "0x3b9aca00" }), { status: 200 });
  }) as unknown as typeof fetch;
  const bal = await readBalances("0x000000000000000000000000000000000000dEaD");
  assert.equal(bal.eth, 1.5);
  assert.equal(bal.usdc, 1000); // 1e9 raw units at 6 decimals
  assert.equal(calls.length, 2);

  globalThis.fetch = (async () => {
    throw new Error("rpc down");
  }) as unknown as typeof fetch;
  const dead = await readBalances("0x000000000000000000000000000000000000dEaD");
  assert.equal(dead.eth, null);
  assert.equal(dead.usdc, null);

  globalThis.fetch = realFetch;

  // The swap allowlist decides which tokens a USDC deposit can buy. Only
  // outbound paths on the active chain count.
  const paths = {
    quote_asset: "USDC",
    paths: [
      { chain_id: 8453, from: "USDC", to: "WETH", hops: 1 },
      { chain_id: 8453, from: "WETH", to: "USDC", hops: 1 }, // the exit, not a target
      { chain_id: 84532, from: "USDC", to: "WETH", hops: 1 }, // other chain
    ],
  };
  const targets = swappableAssets(paths, 8453);
  assert.deepEqual([...targets].sort(), ["USDC", "WETH"]);
  assert.deepEqual([...swappableAssets(paths, 84532)].sort(), ["USDC", "WETH"]);
  assert.equal(isSwappable("cbBTC", targets), false);
  assert.equal(isSwappable("USDC", targets), true);
  // A failed read is unknown, not "nothing is swappable": everything stays
  // selectable rather than the whole token list greying out on a 503.
  assert.equal(isSwappable("cbBTC", null), true);
  // An empty allowlist really is empty, and is not confused with a failure.
  assert.equal(
    isSwappable("WETH", swappableAssets({ quote_asset: "USDC", paths: null })),
    false,
  );

  // "Your status" never claims anything about a caller that does not exist.
  const basket = (x: Partial<BasketSummary>) => x as BasketSummary;
  assert.equal(ownership(basket({})), null);
  assert.equal(ownership(basket({ subscribed: false })), "not joined");
  assert.equal(ownership(basket({ subscribed: true })), "joined");
  assert.equal(ownership(basket({ created_by_me: true, subscribed: false })), "created");
  assert.equal(
    ownership(basket({ created_by_me: true, subscribed: true })),
    "created · joined",
  );

  // Chain separation: a testnet row must never be counted on mainnet, and an
  // older differently-cased label must not orphan its row.
  assert.equal(sameChain("base", "base"), true);
  assert.equal(sameChain("Base", "base"), true);
  assert.equal(sameChain("base", "base-sepolia"), false);
  assert.equal(sameChain("", "base"), false);
  assert.equal(sameChain(undefined, "base"), false);

  console.log("api.check: all assertions passed");
}

void main();
