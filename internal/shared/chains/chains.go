// Package chains is the one place that maps between a chain's numeric id and
// the label used in venue ids, the market-data API and the executor allowlist.
//
// The label is load-bearing: a venue id is "<label>:<project>:<pool>", so a
// label that disagrees with the allowlist silently makes a venue unroutable —
// or, far worse, makes a testnet venue look like a mainnet one. There is
// exactly one spelling per chain here, and no fuzzy matching: an unknown label
// resolves to nothing and the caller must fail loudly.
package chains

import "strings"

const (
	BaseMainnet = 8453
	BaseSepolia = 84532
)

// Canonical labels. Lowercase and hyphenated so they survive a URL query
// parameter, a Redis key and a JSON id unchanged.
const (
	LabelBaseMainnet = "base"
	LabelBaseSepolia = "base-sepolia"
)

var byLabel = map[string]int{
	LabelBaseMainnet: BaseMainnet,
	LabelBaseSepolia: BaseSepolia,
}

var byID = map[int]string{
	BaseMainnet: LabelBaseMainnet,
	BaseSepolia: LabelBaseSepolia,
}

// Supported lists the chain ids this backend serves, mainnet first.
func Supported() []int { return []int{BaseMainnet, BaseSepolia} }

// ID resolves a label to its chain id. Case is ignored — a request carrying
// "Base" means mainnet — but nothing else is guessed.
func ID(label string) (int, bool) {
	id, ok := byLabel[strings.ToLower(strings.TrimSpace(label))]
	return id, ok
}

// Label is the canonical spelling for a chain id.
func Label(id int) (string, bool) {
	l, ok := byID[id]
	return l, ok
}

// Normalize rewrites any accepted spelling of a label into the canonical one.
func Normalize(label string) (string, bool) {
	id, ok := ID(label)
	if !ok {
		return "", false
	}
	return byID[id], true
}
