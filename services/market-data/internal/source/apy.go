package source

import (
	"math"
	"math/big"
	"time"
)

// Every protocol expresses yield in its own unit. This file is the single place
// those units become one number: APY as a percentage (5.25 means 5.25%).
//
// All conversions use math.Expm1(n*math.Log1p(r)) rather than math.Pow(1+r, n).
// With per-second rates r is ~1e-9, and 1+r loses most of its significant digits
// in float64; Log1p/Expm1 keep them.

// SecondsPerYear is the constant Aave and Compound III use (365 days).
const SecondsPerYear = 31_536_000

// ray is 1e27, the fixed-point scale Aave uses for interest rates.
var ray = new(big.Float).SetFloat64(1e27)

// RayToAPR converts an Aave V3 `liquidityRate` ray into a decimal APR.
//
// In: a ray-scaled integer where 1e27 == 100% annual simple interest.
// Out: APR as a fraction (0.0172 == 1.72%).
// Verified against live Base data: WETH liquidityRate 17234812126058827297172826
// -> 0.017234..., matching Aave's reported supply APR.
func RayToAPR(rayRate *big.Int) float64 {
	if rayRate == nil || rayRate.Sign() <= 0 {
		return 0
	}
	// Divide in big.Float: a 27-decimal integer does not survive float64 intact.
	apr, _ := new(big.Float).Quo(new(big.Float).SetInt(rayRate), ray).Float64()
	return apr
}

// APRToAPY compounds a simple annual rate per second, the way Aave's own UI does.
//
// In: APR as a fraction. Out: APY percent.
//
//	APY = ((1 + APR/secondsPerYear)^secondsPerYear - 1) * 100
func APRToAPY(apr float64) float64 { return compound(apr/SecondsPerYear, SecondsPerYear) }

// RayRateToAPY is the two steps above composed: ray -> APY percent.
func RayRateToAPY(rayRate *big.Int) float64 { return APRToAPY(RayToAPR(rayRate)) }

// PerSecondMantissaToAPY converts a per-second rate to an APY percentage.
//
// In: a per-second growth rate scaled by `mantissa` (Compound III / Moonwell
// use 1e18), i.e. the fraction of principal earned each second.
// Out: APY percent, compounding every second.
func PerSecondMantissaToAPY(rate *big.Int, mantissa float64) float64 {
	return perPeriodToAPY(rate, mantissa, SecondsPerYear)
}

// PerBlockRateToAPY converts a Compound V2 style per-block rate to an APY percentage.
//
// In: a per-block growth rate scaled by `mantissa` (1e18), plus the chain's
// blocks-per-year (Base at 2s blocks: 15_768_000).
// Out: APY percent, compounding every block.
func PerBlockRateToAPY(rate *big.Int, mantissa, blocksPerYear float64) float64 {
	return perPeriodToAPY(rate, mantissa, blocksPerYear)
}

// SharePriceGrowthToAPY annualizes realized vault-share appreciation.
//
// In: the share price (assets per share, any consistent fixed-point scale) at
// two points in time, and the elapsed period between them.
// Out: APY percent, compounding the observed growth over a full year.
// This is the convention ERC-4626 vaults such as Morpho use, where there is no
// rate field at all — yield is only visible as price drift.
func SharePriceGrowthToAPY(before, after *big.Int, elapsed time.Duration) float64 {
	if before == nil || after == nil || before.Sign() <= 0 || elapsed <= 0 {
		return 0
	}
	growth, _ := new(big.Float).Quo(new(big.Float).SetInt(after), new(big.Float).SetInt(before)).Float64()
	periods := float64(365*24*time.Hour) / float64(elapsed)
	return compound(growth-1, periods)
}

func perPeriodToAPY(rate *big.Int, mantissa, periods float64) float64 {
	if rate == nil || rate.Sign() <= 0 || mantissa <= 0 {
		return 0
	}
	r, _ := new(big.Float).Quo(new(big.Float).SetInt(rate), big.NewFloat(mantissa)).Float64()
	return compound(r, periods)
}

// compound turns a per-period rate into a compounded annual percentage.
func compound(rate, periods float64) float64 {
	if rate <= 0 || periods <= 0 {
		return 0
	}
	apy := math.Expm1(periods*math.Log1p(rate)) * 100
	if math.IsInf(apy, 0) || math.IsNaN(apy) {
		return 0
	}
	return apy
}
