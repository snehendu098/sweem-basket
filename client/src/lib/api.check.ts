import assert from "node:assert/strict";
import {
  ApiError,
  DEFAULT_CHAIN_ID,
  isSwappable,
  swappableAssets,
  assetWarning,
  venuesById,
  assetGroups,
  groupOf,
  reachableAssets,
  reachableVenues,
  remapSelection,
  venueWarning,
  EMISSIONS_WARNING,
  NO_YIELD_WARNING,
  UNKNOWN_YIELD_WARNING,
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
  marketAssets,
  publicBaskets,
  walletFetch,
} from "./api";
import { ownership } from "./types";
import type { AssetSummary, BasketSummary, Family, Venue } from "./types";

type Stub = { status: number; body: unknown };
const realFetch = globalThis.fetch;
function stub(s: Stub) {
  globalThis.fetch = (async () =>
    new Response(JSON.stringify(s.body), { status: s.status })) as typeof fetch;
}

async function main() {
  stub({
    status: 207,
    body: { failed_legs: 1, pending_legs: 1, legs: [{ status: "failed" }] },
  });
  const res = await walletFetch<{ failed_legs: number }>("/v1/x", "tok");
  assert.equal(res.status, 207);
  assert.equal(res.data.failed_legs, 1);

  stub({ status: 412, body: { error: "cannot execute: wallet delegation is missing" } });
  await assert.rejects(
    () => walletFetch("/v1/x", "tok"),
    (e: unknown) =>
      e instanceof ApiError &&
      e.status === 412 &&
      e.message.includes("delegation is missing"),
  );

  stub({ status: 404, body: { error: { code: "no_venue", message: "no venue matches asset=DOGE" } } });
  await assert.rejects(
    () => marketAssets(),
    (e: unknown) => e instanceof ApiError && e.message.includes("DOGE"),
  );

  stub({ status: 200, body: { data: { assets: [{ asset: "USDC" }], count: 1 } } });
  const assets = await marketAssets();
  assert.equal(assets.count, 1);

  assert.equal(fmtUsdOrUnknown(null), "value unknown");
  assert.equal(fmtUsdOrUnknown(undefined), "value unknown");
  assert.equal(fmtUsdOrUnknown(0), "$0.00");
  assert.equal(fmtUsd(1234.5), "$1,234.50");

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
  // 0% is a rate, absent is unknown: a zero leg must dilute, not vanish.
  assert.equal(
    blendApy(
      [
        { asset: "USDC", weight_bps: 5000 },
        { asset: "AERO", weight_bps: 5000 },
      ],
      [...summaries, { asset: "AERO", best_apy: 0 } as AssetSummary],
    ),
    5,
  );
  assert.equal(blendApy([{ asset: "PYUSD", weight_bps: 10000 }], summaries), null);
  assert.equal(blendApy([], summaries), null);
  assert.equal(blendApy([{ asset: "USDC", weight_bps: 10000 }], null), null);

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
  assert.deepEqual(
    evenSplit(["USDC", "WETH", "DAI"], 100).map((w) => w.weight_bps),
    [34, 33, 33],
  );

  assert.equal(displayAsset("WETH"), "ETH");
  assert.equal(displayAsset("USDC"), "USDC");
  assert.equal(evenSplit(["WETH"])[0].asset, "WETH");

  stub({ status: 200, body: { data: [{ id: "a" }] } });
  assert.equal((await publicBaskets()).length, 1);
  stub({ status: 200, body: { data: { baskets: [{ id: "a" }, { id: "b" }] } } });
  assert.equal((await publicBaskets()).length, 2);

  setActiveChainId(8453);
  assert.equal(activeChain().label, "base");
  assert.equal(activeChain().testnet, false);
  assert.ok(basescanTx("0xabc").startsWith("https://basescan.org/tx/"));
  setActiveChainId(84532);
  assert.equal(activeChain().label, "base-sepolia");
  assert.equal(activeChain().testnet, true);
  assert.ok(basescanTx("0xabc").startsWith("https://sepolia.basescan.org/tx/"));
  setActiveChainId(DEFAULT_CHAIN_ID);

  const calls: string[] = [];
  globalThis.fetch = (async (_url: string, init: RequestInit) => {
    const req = JSON.parse(String(init.body)) as {
      method: string;
      params: unknown[];
    };
    calls.push(req.method);
    if (req.method === "eth_getBalance") {
      return new Response(
        JSON.stringify({ result: "0x14d1120d7b160000" }),
        { status: 200 },
      );
    }
    const call = req.params[0] as { data: string };
    assert.equal(
      call.data,
      "0x70a08231000000000000000000000000000000000000000000000000000000000000dead",
    );
    return new Response(JSON.stringify({ result: "0x3b9aca00" }), { status: 200 });
  }) as unknown as typeof fetch;
  const bal = await readBalances("0x000000000000000000000000000000000000dEaD");
  assert.equal(bal.eth, 1.5);
  assert.equal(bal.usdc, 1000);
  assert.equal(calls.length, 2);

  globalThis.fetch = (async () => {
    throw new Error("rpc down");
  }) as unknown as typeof fetch;
  const dead = await readBalances("0x000000000000000000000000000000000000dEaD");
  assert.equal(dead.eth, null);
  assert.equal(dead.usdc, null);

  globalThis.fetch = realFetch;

  const paths = {
    quote_asset: "USDC",
    paths: [
      { chain_id: 8453, from: "USDC", to: "WETH", hops: 1 },
      { chain_id: 8453, from: "WETH", to: "USDC", hops: 1 },
      { chain_id: 84532, from: "USDC", to: "WETH", hops: 1 },
    ],
  };
  const targets = swappableAssets(paths, 8453);
  assert.deepEqual([...targets].sort(), ["USDC", "WETH"]);
  assert.deepEqual([...swappableAssets(paths, 84532)].sort(), ["USDC", "WETH"]);
  assert.equal(isSwappable("cbBTC", targets), false);
  assert.equal(isSwappable("USDC", targets), true);
  assert.equal(isSwappable("cbBTC", null), true);
  assert.equal(
    isSwappable("WETH", swappableAssets({ quote_asset: "USDC", paths: null })),
    false,
  );

  const basket = (x: Partial<BasketSummary>) => x as BasketSummary;
  assert.equal(ownership(basket({})), null);
  assert.equal(ownership(basket({ subscribed: false })), "not joined");
  assert.equal(ownership(basket({ subscribed: true })), "joined");
  assert.equal(ownership(basket({ created_by_me: true, subscribed: false })), "created");
  assert.equal(
    ownership(basket({ created_by_me: true, subscribed: true })),
    "created · joined",
  );

  const venue = (x: Partial<Venue>) => ({ apy_reward: 0, ...x }) as Venue;
  const vmap = venuesById([
    venue({ id: "base:moonwell:usdc", apy_base: 14.5 }),
    venue({ id: "base:aave-v3:usdc", apy_base: 4.2 }),
    venue({ id: "base:hold:wsteth", apy_base: 0 }),
    venue({ id: "base:morpho:eth", apy_base: 12, apy_reward: 3 }),
  ]);
  assert.equal(vmap.size, 4);
  const sum = (x: Partial<AssetSummary>) => x as AssetSummary;
  assert.equal(assetWarning(undefined), UNKNOWN_YIELD_WARNING);
  assert.equal(
    assetWarning(sum({ venues: 0, routable_venues: 0, best_apy: 0 })),
    NO_YIELD_WARNING,
  );
  // An illiquid market is TVL nobody can withdraw: routable_venues is the count.
  assert.equal(
    assetWarning(sum({ venues: 3, routable_venues: 0, best_apy: 0 })),
    NO_YIELD_WARNING,
  );
  assert.equal(
    assetWarning(
      sum({ venues: 1, routable_venues: 1, best_apy: 14.5, best_apy_base: 14.5, best_apy_reward: 0 }),
    ),
    EMISSIONS_WARNING,
  );
  assert.equal(
    assetWarning(
      sum({ venues: 1, routable_venues: 1, best_apy: 14.5, best_apy_base: 2, best_apy_reward: 12.5 }),
    ),
    null,
  );
  assert.equal(
    assetWarning(sum({ venues: 1, routable_venues: 1, best_apy: 4.2, best_apy_base: 4.2, best_apy_reward: 0 })),
    null,
  );
  // Split rate absent is unknown, not zero emissions.
  assert.equal(assetWarning(sum({ venues: 1, routable_venues: 1, best_apy: 9 })), null);

  assert.equal(venueWarning(venue({ apy: 0, apy_base: 0 })), NO_YIELD_WARNING);
  assert.equal(venueWarning(venue({ apy: 14.5, apy_base: 14.5 })), EMISSIONS_WARNING);
  assert.equal(venueWarning(venue({ apy: 4, apy_base: 4 })), null);

  const paths2 = new Set(["USDC", "cbBTC", "WETH"]);
  const reach = [
    sum({ asset: "USDC", family: "USD", best_apy: 5.77, best_venue: "v4" }),
    sum({ asset: "cbBTC", family: "BTC", best_apy: 2.83, best_venue: "v1" }),
    sum({ asset: "WBTC", family: "BTC", best_apy: 0, best_venue: "" }),
    sum({ asset: "tBTC", best_apy: 0.16, best_venue: "v5" }),
  ];
  assert.deepEqual(
    reachableAssets(reach, paths2).map((a) => a.asset),
    ["USDC", "cbBTC"],
  );
  // Unknown paths must not empty the picker.
  assert.equal(reachableAssets(reach, null).length, 4);
  assert.deepEqual(
    reachableVenues(
      [
        venue({ id: "a", asset: "USDC" }),
        venue({ id: "b", asset: "USDC", not_routable: "withdrawable liquidity $0.00" }),
        venue({ id: "c", asset: "tBTC" }),
      ],
      paths2,
    ).map((v) => v.id),
    ["a"],
  );
  assert.equal(reachableVenues([venue({ id: "b", asset: "USDC", not_routable: "x" })], null).length, 0);

  const fams: Family[] = [
    { family: "BTC", chain: "base", instruments: ["WBTC", "cbBTC"], best_asset: "cbBTC", best_apy: 2.83, best_venue: "v1", venues: 2 },
    { family: "USD", chain: "base", instruments: ["USDC"], best_asset: "USDC", best_apy: 5.77, best_venue: "v4", venues: 1 },
  ];
  const famAssets = [
    ...reach.slice(0, 3),
    sum({ asset: "LINK", best_apy: 0.5, best_venue: "v3" }),
  ];
  const gs = assetGroups(famAssets, fams);
  assert.deepEqual(
    gs.map((g) => [g.id, g.family, g.best.asset]),
    [
      ["USD", true, "USDC"],
      ["BTC", true, "cbBTC"],
      ["LINK", false, "LINK"],
    ],
  );
  // Hiding an unreachable instrument moves the family winner, it does not hide the family.
  const reachable = assetGroups(reachableAssets(famAssets, paths2), fams);
  assert.deepEqual(
    reachable.find((g) => g.id === "BTC")?.members.map((m) => m.asset),
    ["cbBTC"],
  );
  // No families array degrades to today's flat instrument list.
  const flat = assetGroups(
    famAssets.map((a) => sum({ ...a, family: undefined })),
    null,
  );
  assert.deepEqual(
    flat.map((g) => [g.id, g.family]),
    [["USDC", false], ["cbBTC", false], ["LINK", false], ["WBTC", false]],
  );

  assert.equal(groupOf("WBTC", gs)?.id, "BTC");
  assert.equal(groupOf("AERO", gs), undefined);

  // Two venues of one family collapse to one row, not a drop.
  assert.deepEqual(
    remapSelection(["v1", "v2", "v9"], (id) =>
      id === "v9" ? null : groupOf(id === "v1" ? "cbBTC" : "WBTC", gs)?.id ?? null,
    ),
    { kept: ["BTC"], dropped: ["v9"] },
  );
  assert.deepEqual(remapSelection([], () => null), { kept: [], dropped: [] });

  assert.equal(sameChain("base", "base"), true);
  assert.equal(sameChain("Base", "base"), true);
  assert.equal(sameChain("base", "base-sepolia"), false);
  assert.equal(sameChain("", "base"), false);
  assert.equal(sameChain(undefined, "base"), false);

  console.log("api.check: all assertions passed");
}

void main();
