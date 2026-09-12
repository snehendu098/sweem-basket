package source

import (
	"log/slog"
	"math/big"
	"time"
)

type GrowthSample struct {
	At    time.Time
	Value *big.Int
}

// ok=false means "no rate" and callers must not turn it into 0%: that ranks a
// 3.1% wstETH position below a 1.2% USDC one.
func AnnualizeGrowth(label string, samples []GrowthSample, minWindow, maxWindow time.Duration) (float64, bool) {
	usable := samples[:0:0]
	for _, s := range samples {
		if s.Value != nil && s.Value.Sign() > 0 && !s.At.IsZero() {
			usable = append(usable, s)
		}
	}
	if len(usable) < 2 {
		slog.Debug("growth: no rate, fewer than 2 usable samples", "series", label, "usable", len(usable))
		return 0, false
	}

	newest := usable[0]
	for _, s := range usable {
		if s.At.After(newest.At) {
			newest = s
		}
	}
	var oldest GrowthSample
	for _, s := range usable {
		if !s.At.Before(newest.At) || newest.At.Sub(s.At) > maxWindow {
			continue
		}
		if oldest.Value == nil || s.At.Before(oldest.At) {
			oldest = s
		}
	}
	if oldest.Value == nil {
		slog.Debug("growth: no rate, no second sample inside max window", "series", label, "max", maxWindow)
		return 0, false
	}

	elapsed := newest.At.Sub(oldest.At)
	if elapsed < minWindow {
		slog.Debug("growth: no rate, sampling window too short",
			"series", label, "elapsed", elapsed, "min", minWindow)
		return 0, false
	}
	if newest.Value.Cmp(oldest.Value) < 0 {
		slog.Warn("growth: no rate, value fell",
			"series", label, "before", oldest.Value, "after", newest.Value, "elapsed", elapsed)
		return 0, false
	}
	apy := SharePriceGrowthToAPY(oldest.Value, newest.Value, elapsed)
	if apy <= 0 {
		slog.Debug("growth: no rate, no growth over the window", "series", label, "elapsed", elapsed)
		return 0, false
	}
	return apy, true
}
