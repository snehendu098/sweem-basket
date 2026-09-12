package source

import (
	"strings"
	"testing"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
)

var (
	mainnet = Chain{Label: chains.LabelBaseMainnet, ID: chains.BaseMainnet}
	sepolia = Chain{Label: chains.LabelBaseSepolia, ID: chains.BaseSepolia}
)

// Subgraph deployments are per chain. Configuring one chain must never make the
// other chain's adapter "work" — it would serve the wrong network's venues.
func TestSubgraphIDIsPerChainAndNeverFallsBack(t *testing.T) {
	t.Setenv("AAVE_V3_SUBGRAPH_ID_8453", "mainnet-deployment")
	t.Setenv("AAVE_V3_SUBGRAPH_ID_84532", "")
	// The old chain-agnostic variable must be inert, not a fallback.
	t.Setenv("AAVE_V3_SUBGRAPH_ID", "legacy-value")

	if got := SubgraphID("AAVE_V3", mainnet); got != "mainnet-deployment" {
		t.Fatalf("mainnet id = %q", got)
	}
	if got := SubgraphID("AAVE_V3", sepolia); got != "" {
		t.Fatalf("sepolia id = %q, want empty: an unconfigured chain must report unconfigured", got)
	}
}

// An unconfigured adapter stays in the list so /sources can report it, and the
// query names the exact variable to set.
func TestUnconfiguredAdapterIsListedAndNamesItsEnvVar(t *testing.T) {
	for _, k := range []string{"AAVE_V3", "COMPOUND_V3", "MOONWELL", "MORPHO"} {
		t.Setenv(k+"_SUBGRAPH_ID_84532", "")
	}
	adapters := Adapters(sepolia)
	if len(adapters) == 0 {
		t.Fatal("no adapters registered")
	}
	g := &GraphSource{Chain: sepolia, Adapters: adapters, status: map[string]Status{}}
	for _, st := range g.Status() {
		if st.OK {
			t.Fatalf("%s reported ok without a subgraph id", st.Protocol)
		}
		if !strings.Contains(st.Error, "_SUBGRAPH_ID_84532") {
			t.Fatalf("%s error %q must name the missing per-chain variable", st.Protocol, st.Error)
		}
		if st.ChainID != chains.BaseSepolia || st.Chain != chains.LabelBaseSepolia {
			t.Fatalf("%s status is not tagged with its chain: %+v", st.Protocol, st)
		}
	}
}

// A full URL is used verbatim (Studio), a bare id hangs off the gateway base.
func TestSubgraphURLAcceptsBothStudioAndGateway(t *testing.T) {
	g := &GraphSource{Gateway: DefaultGatewayURL}
	if got := g.url("QmBareDeploymentID"); got != DefaultGatewayURL+"/QmBareDeploymentID" {
		t.Fatalf("bare id url = %q", got)
	}
	studio := "https://api.studio.thegraph.com/query/1/aave-v-3-base-sepolia/v0.0.1"
	if got := g.url(studio); got != studio {
		t.Fatalf("studio url = %q, want it used verbatim", got)
	}
}
