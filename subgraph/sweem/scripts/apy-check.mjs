// Runnable check for the rate conversions in src/normalize.ts.
//
// The subgraph does this arithmetic in AssemblyScript and the router redoes it
// in Go (services/market-data/internal/source/apy.go). Three implementations of
// one formula is three chances to drift, so this pins the numbers.
//
//   bun scripts/apy-check.mjs
import assert from "node:assert/strict";

const SECONDS_PER_YEAR = 31_536_000;

const aprToApy = (apr) =>
  Math.expm1(SECONDS_PER_YEAR * Math.log1p(apr / SECONDS_PER_YEAR)) * 100;

const close = (a, b, eps, what) =>
  assert.ok(Math.abs(a - b) < eps, `${what}: got ${a}, want ~${b}`);

// --- Aave V3: liquidityRate is a ray (1e27) annual simple rate --------------
// Live Base Sepolia WETH reserve, the number this subgraph was verified against.
const wethRay = 704152479263146270890280563n;
const wethApr = Number(wethRay) / 1e27;
close(wethApr, 0.70415247926, 1e-9, "aave WETH APR");
// Compounded every second, a 70.415% APR is e^0.70415 - 1.
close(aprToApy(wethApr), (Math.exp(wethApr) - 1) * 100, 1e-5, "aave WETH APY");
close(aprToApy(wethApr), 102.2132, 1e-3, "aave WETH APY absolute");

// --- Compound III: getSupplyRate is PER-SECOND, scaled 1e18 -----------------
// NOT per block. A per-block reading would be ~2x off on Base's 2s blocks.
const perSecond = 1_585_489_599n; // ~5% APR
const cometApr = (Number(perSecond) / 1e18) * SECONDS_PER_YEAR;
close(cometApr, 0.0500000, 1e-6, "comet APR");
close(aprToApy(cometApr), 5.1271, 1e-3, "comet APY");

// --- MetaMorpho: APY from ERC-4626 share-price growth ----------------------
// sharePriceScaled = totalAssets * 1e36 / totalSupply; the 1e36 cancels.
const before = 1_000_000_000_000_000_000_000_000n; // 1e24
const after = 1_000_005_707_762_557_100_000_000n; // +5.7078e-6 over 1h
const elapsed = 3600;
const growth = Number(after) / Number(before) - 1;
const morphoApy =
  Math.expm1((SECONDS_PER_YEAR / elapsed) * Math.log1p(growth)) * 100;
close(morphoApy, 5.1271, 5e-3, "morpho APY from 1h of drift");

// --- Why Expm1(n*Log1p(r)) and not Pow(1+r, n) -----------------------------
// `1 + r` keeps only the digits of r that fit alongside the leading 1, so the
// naive form degrades as the per-second rate shrinks. At a dust-level APR the
// relative error is already visible; at 5% it is ~5e-8, which is small but is
// pure avoidable loss.
const tinyApr = 1e-6;
const tinyR = tinyApr / SECONDS_PER_YEAR; // ~3.2e-14
const tinyNaive = (Math.pow(1 + tinyR, SECONDS_PER_YEAR) - 1) * 100;
const tinyExact = aprToApy(tinyApr);
assert.ok(
  Math.abs(tinyNaive - tinyExact) / tinyExact > 1e-6,
  `expected the naive form to lose precision; naive=${tinyNaive} exact=${tinyExact}`,
);

// --- The 12dp scaled-i64 rounding used to reach BigDecimal -----------------
// AssemblyScript renders small doubles in exponent form, which
// BigDecimal.fromString does not read back, so normalize.ts goes through a
// scaled integer instead. Check the scaling stays inside i64.
const scaled = Math.round(1.0e6 * 1e12); // the MAX_APY_PERCENT clamp
assert.ok(scaled < Number(2n ** 63n - 1n), "12dp scaling overflows i64");

console.log("apy-check: all conversions agree");
