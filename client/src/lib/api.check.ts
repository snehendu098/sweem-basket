/**
 * Self-check for the rules that must not silently regress:
 *   - 207 Multi-Status is a result, not a thrown error
 *   - both services' error envelopes are unwrapped to a real message
 *   - a null price renders as "value unknown", never a fabricated dollar
 *
 * Run: bun run src/lib/api.check.ts
 */
import assert from "node:assert/strict";
import { ApiError, fmtUsd, fmtUsdOrUnknown, market, walletFetch } from "./api";

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

  globalThis.fetch = realFetch;
  console.log("api.check: all assertions passed");
}

void main();
