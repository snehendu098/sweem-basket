package source

import (
	"math"
	"math/big"
	"testing"
	"time"
)

func bi(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad big int %q", s)
	}
	return v
}

func close(t *testing.T, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("got %.8f, want %.8f (tol %g)", got, want, tol)
	}
}

func TestRayRateToAPY(t *testing.T) {
	tests := []struct {
		name string
		ray  string
		want float64 // hand-computed: (1 + apr/31536000)^31536000 - 1, in percent
	}{
		// 4.2% APR ray = 0.042 * 1e27. e^0.042 - 1 = 0.0428944 -> 4.28944%
		{"4.2% apr", "42000000000000000000000000", 4.289442},
		// 100% APR: e^1 - 1 = 1.7182818 -> 171.82818%
		{"100% apr", "1000000000000000000000000000", 171.828183},
		// 0.000001% APR stays ~ itself: precision check on a tiny ray
		{"1e-8 apr", "10000000000000000000", 0.000001},
		{"zero", "0", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			close(t, RayRateToAPY(bi(t, tc.ray)), tc.want, 1e-5)
		})
	}
	if got := RayRateToAPY(nil); got != 0 {
		t.Errorf("nil ray = %v, want 0", got)
	}
	if got := RayRateToAPY(big.NewInt(-1)); got != 0 {
		t.Errorf("negative ray = %v, want 0", got)
	}
}

func TestPerSecondMantissaToAPY(t *testing.T) {
	tests := []struct {
		name     string
		rate     string
		mantissa float64
		want     float64
	}{
		// 1e9 / 1e18 = 1e-9 per second. (1+1e-9)^31536000 - 1 = 3.20385%
		{"compound iii typical", "1000000000", 1e18, 3.203853},
		// 3.17e-10/s ~ 1% simple APR -> e^0.01 - 1 = 1.005017%
		{"1% apr per second", "317097919", 1e18, 1.005013},
		{"zero", "0", 1e18, 0},
		{"bad mantissa", "1000000000", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			close(t, PerSecondMantissaToAPY(bi(t, tc.rate), tc.mantissa), tc.want, 1e-5)
		})
	}
}

func TestPerBlockRateToAPY(t *testing.T) {
	// Base at 2s blocks: 15_768_000 blocks/year.
	// 2e9/1e18 = 2e-9 per block -> (1+2e-9)^15768000 - 1 = 3.20385%
	close(t, PerBlockRateToAPY(bi(t, "2000000000"), 1e18, 15_768_000), 3.203853, 1e-5)
	if got := PerBlockRateToAPY(bi(t, "0"), 1e18, 15_768_000); got != 0 {
		t.Errorf("zero rate = %v, want 0", got)
	}
}

func TestSharePriceGrowthToAPY(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		elapsed       time.Duration
		want          float64
	}{
		// +1% over 30 days -> 1.01^(365/30) - 1 = 12.8695%
		{"1% in 30 days", "1000000000000000000", "1010000000000000000", 30 * 24 * time.Hour, 12.869529}, // 1.01^(365/30)-1
		// +5% over a full year is just 5%
		{"5% in a year", "1000000000000000000", "1050000000000000000", 365 * 24 * time.Hour, 5},
		{"flat", "1000000000000000000", "1000000000000000000", 24 * time.Hour, 0},
		{"negative growth", "1000000000000000000", "990000000000000000", 24 * time.Hour, 0},
		{"zero elapsed", "1000000000000000000", "1010000000000000000", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			close(t, SharePriceGrowthToAPY(bi(t, tc.before), bi(t, tc.after), tc.elapsed), tc.want, 1e-4)
		})
	}
	if got := SharePriceGrowthToAPY(nil, nil, time.Hour); got != 0 {
		t.Errorf("nil prices = %v, want 0", got)
	}
}
