package store

import "testing"

// A family answers "which instrument wins right now"; an instrument with no
// routable venue cannot win it, and singletons are not a family.
func TestFamiliesPickTheBestRoutableInstrument(t *testing.T) {
	assets := []AssetSummary{
		{Asset: "wstETH", Chain: "base", Family: "ETH", Venues: 2, Routable: 2, BestAPY: 3.10, BestVenue: "base:aave-v3:0xc1"},
		{Asset: "weETH", Chain: "base", Family: "ETH", Venues: 1, Routable: 1, BestAPY: 3.15, BestVenue: "base:aave-v3:0x04"},
		{Asset: "WETH", Chain: "base", Family: "ETH", Venues: 1, Routable: 0, BestAPY: 0},
		{Asset: "AERO", Chain: "base", Family: "", Venues: 1, Routable: 1, BestAPY: 9, BestVenue: "base:moonwell:0x73"},
	}
	fams := Families(assets)
	if len(fams) != 1 {
		t.Fatalf("got %d families, want 1 (AERO is a singleton, not a family)", len(fams))
	}
	f := fams[0]
	if f.Family != "ETH" || f.BestAsset != "weETH" || f.BestVenue != "base:aave-v3:0x04" {
		t.Fatalf("family = %+v", f)
	}
	if len(f.Instruments) != 3 {
		t.Fatalf("instruments = %v, want all three listed even when one is unroutable", f.Instruments)
	}
	if f.Venues != 3 {
		t.Fatalf("routable venue count = %d, want 3", f.Venues)
	}
}

// An instrument whose only venue is illiquid must not win its family.
func TestFamilySkipsInstrumentWithNoRoutableVenue(t *testing.T) {
	fams := Families([]AssetSummary{
		{Asset: "USDC", Chain: "base", Family: "USD", Venues: 1, Routable: 0, BestAPY: 0},
		{Asset: "USDS", Chain: "base", Family: "USD", Venues: 1, Routable: 1, BestAPY: 4.1, BestVenue: "base:compound-v3:0x2c"},
	})
	if len(fams) != 1 || fams[0].BestAsset != "USDS" {
		t.Fatalf("families = %+v", fams)
	}
}
