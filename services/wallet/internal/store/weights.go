package store

import (
	"errors"
	"fmt"
)

// TotalBps is the required sum of a basket's weights.
const TotalBps = 10000

// ValidateWeights enforces the basket invariant: at least one asset, no
// duplicates, every weight positive, and the whole thing summing to 10000 bps.
// Kept separate from the DB because Postgres has no cross-row CHECK.
func ValidateWeights(ws []Weight) error {
	if len(ws) == 0 {
		return errors.New("basket: needs at least one weight")
	}
	seen := make(map[string]bool, len(ws))
	sum := 0
	for _, w := range ws {
		if w.Asset == "" {
			return errors.New("basket: empty asset")
		}
		if seen[w.Asset] {
			return fmt.Errorf("basket: duplicate asset %q", w.Asset)
		}
		seen[w.Asset] = true
		if w.WeightBps <= 0 || w.WeightBps > TotalBps {
			return fmt.Errorf("basket: weight for %s out of range: %d", w.Asset, w.WeightBps)
		}
		sum += w.WeightBps
	}
	if sum != TotalBps {
		return fmt.Errorf("basket: weights sum to %d, want %d", sum, TotalBps)
	}
	return nil
}
