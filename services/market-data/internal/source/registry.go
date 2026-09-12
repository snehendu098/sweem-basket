package source

import (
	"fmt"
	"log/slog"

	"github.com/snehendu098/sweem-basket/internal/shared/config"
)

// Chain is the pair every adapter needs: the label that goes into venue ids,
// and the numeric id that selects the subgraph and the price feed table.
type Chain struct {
	Label string
	ID    int
}

// Adding a protocol is one new file: define the adapter and call Register from
// its init(). Nothing else in the package changes.
var registry []func(c Chain) ProtocolAdapter

// Register adds an adapter constructor to the registry.
func Register(build func(c Chain) ProtocolAdapter) { registry = append(registry, build) }

// Adapters builds every registered adapter for the given chain. An adapter with
// no configured subgraph id is kept in the list so /sources reports it as
// unconfigured — hiding it would make a missing mainnet id look like a chain
// with no venues.
func Adapters(c Chain) []ProtocolAdapter {
	out := make([]ProtocolAdapter, 0, len(registry))
	for _, build := range registry {
		a := build(c)
		if a.SubgraphID() == "" {
			slog.Warn("adapter unconfigured: no subgraph id for this chain, it will report as failed",
				"protocol", a.Protocol(), "chain", c.Label, "chain_id", c.ID)
		}
		out = append(out, a)
	}
	return out
}

// SubgraphEnvKey is the per-protocol-per-chain variable name, e.g.
// AAVE_V3_SUBGRAPH_ID_8453. Deployments are per chain, so the configuration is
// too: there is no chain-agnostic variable to fall back to, because falling
// back is how a mainnet deployment ends up serving testnet venues.
func SubgraphEnvKey(prefix string, c Chain) string {
	return fmt.Sprintf("%s_SUBGRAPH_ID_%d", prefix, c.ID)
}

// SubgraphID reads that variable. Unset means unconfigured, never a default.
func SubgraphID(prefix string, c Chain) string {
	return config.GetEnv(SubgraphEnvKey(prefix, c), "")
}
