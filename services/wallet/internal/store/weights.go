package store

import (
	"errors"
	"fmt"
	"strings"
)

const TotalBps = 10000

// Names only. Which instruments belong to a family is market-data's table, not
// a second copy here.
var families = map[string]bool{"USD": true, "EUR": true, "ETH": true, "BTC": true}

func IsFamily(asset string) bool { return families[strings.ToUpper(strings.TrimSpace(asset))] }

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
		if w.VenueID != "" && IsFamily(w.Asset) {
			return fmt.Errorf("basket: weight for family %s cannot also pin venue %s: a pin already names the instrument", w.Asset, w.VenueID)
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
