package source

import (
	"fmt"
	"log/slog"

	"github.com/snehendu098/sweem-basket/internal/shared/config"
)

type Chain struct {
	Label string
	ID    int
}

var registry []func(c Chain) ProtocolAdapter

func Register(build func(c Chain) ProtocolAdapter) { registry = append(registry, build) }

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

func SubgraphEnvKey(prefix string, c Chain) string {
	return fmt.Sprintf("%s_SUBGRAPH_ID_%d", prefix, c.ID)
}

// Per chain, with no chain-agnostic fallback: defaulting an unset testnet id to
// the mainnet deployment serves mainnet venues on the testnet.
func SubgraphID(prefix string, c Chain) string {
	return config.GetEnv(SubgraphEnvKey(prefix, c), "")
}
