package source

import (
	"math"
	"math/big"
	"time"
)

const SecondsPerYear = 31_536_000

var ray = new(big.Float).SetFloat64(1e27)

func RayToAPR(rayRate *big.Int) float64 {
	if rayRate == nil || rayRate.Sign() <= 0 {
		return 0
	}
	apr, _ := new(big.Float).Quo(new(big.Float).SetInt(rayRate), ray).Float64()
	return apr
}

func APRToAPY(apr float64) float64 { return compound(apr/SecondsPerYear, SecondsPerYear) }

func RayRateToAPY(rayRate *big.Int) float64 { return APRToAPY(RayToAPR(rayRate)) }

var rayOne, _ = new(big.Int).SetString("1000000000000000000000000000", 10)

func RayPerSecondFactorToAPY(factor *big.Int) float64 {
	if factor == nil || factor.Cmp(rayOne) <= 0 {
		return 0
	}
	delta := new(big.Float).SetInt(new(big.Int).Sub(factor, rayOne))
	r, _ := new(big.Float).Quo(delta, ray).Float64()
	return compound(r, SecondsPerYear)
}

func PerSecondMantissaToAPY(rate *big.Int, mantissa float64) float64 {
	return perPeriodToAPY(rate, mantissa, SecondsPerYear)
}

// Per BLOCK (Compound V2 forks), not per second. Passing a per-second rate here
// overstates APY by the block time.
func PerBlockRateToAPY(rate *big.Int, mantissa, blocksPerYear float64) float64 {
	return perPeriodToAPY(rate, mantissa, blocksPerYear)
}

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

// Expm1(n*Log1p(r)), not Pow(1+r, n): per-second r is ~1e-9 and 1+r loses most
// of its significant digits in float64.
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
