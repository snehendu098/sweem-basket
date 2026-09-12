package source

import (
	"log/slog"
	"math/big"
	"time"
)

// Two different things in this service are measured the same way: a MetaMorpho
// vault publishes no rate, only a drifting share price, and a liquid staking
// token publishes no rate either, only a drifting exchange rate. Both arrive as
// an irregular series of (timestamp, scaled integer) samples that has to become
// one APY. That shared logic lives here so there is exactly one set of rules
// about which samples are trustworthy — and one place to test them.

// GrowthSample is one observation of a monotonically rising quantity: an
// ERC-4626 share price, an LST exchange rate, a savings-rate accumulator. The
// fixed-point scale does not matter as long as it is the same across samples,
// because only the ratio is ever used.
type GrowthSample struct {
	At    time.Time
	Value *big.Int
}

// AnnualizeGrowth turns a series of samples into an APY percentage.
//
// It picks the WIDEST pair inside maxWindow, because a day of drift is far less
// noisy than six hours, and refuses to answer at all in every case where the
// answer would not be trustworthy:
//
//   - fewer than two usable samples: one point is not a rate;
//   - no second sample inside maxWindow: the old one is not evidence about now;
//   - window narrower than minWindow: annualizing minutes of drift is nonsense;
//   - the value fell: a share price or an exchange rate must not, so either the
//     position took a loss or the feed is broken, and we route on neither;
//   - zero growth over a full window: a stale feed, not a zero-yield asset.
//
// ok=false means "no rate", which the caller must NOT turn into 0%. Reporting
// an unmeasured rate as zero is exactly how a 3.1% wstETH position gets
// "improved" into a 1.2% one.
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

	// Ordering is the server's promise, not ours: pick the extremes explicitly.
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
