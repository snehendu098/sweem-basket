package source

import "log/slog"

// Adding a protocol is one new file: define the adapter and call Register from
// its init(). Nothing else in the package changes.
var registry []func(chain string) ProtocolAdapter

// Register adds an adapter constructor to the registry.
func Register(build func(chain string) ProtocolAdapter) { registry = append(registry, build) }

// Adapters builds every registered adapter for the given chain, skipping any
// whose subgraph id is not configured.
func Adapters(chain string) []ProtocolAdapter {
	out := make([]ProtocolAdapter, 0, len(registry))
	for _, build := range registry {
		a := build(chain)
		if a.SubgraphID() == "" {
			// Kept in the list on purpose: GraphSource records "no subgraph id
			// configured" for it, so GET /sources reports it as unconfigured
			// instead of hiding it. One unconfigured adapter never fails a cycle.
			slog.Warn("adapter unconfigured: no subgraph id, it will report as failed", "protocol", a.Protocol())
		}
		out = append(out, a)
	}
	return out
}
