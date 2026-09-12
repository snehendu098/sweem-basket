package store

import "testing"

// The basket list renders allocations, so weights must come back grouped by
// basket and in the order the query returned them.
func TestGroupWeights(t *testing.T) {
	tests := []struct {
		name string
		rows []basketWeight
		want map[string][]Weight
	}{
		{
			name: "two baskets interleaved by the query's ordering",
			rows: []basketWeight{
				{"b1", "USDC", 6000, ""},
				{"b1", "WETH", 4000, ""},
				{"b2", "DAI", 10000, ""},
			},
			want: map[string][]Weight{
				"b1": {{Asset: "USDC", WeightBps: 6000}, {Asset: "WETH", WeightBps: 4000}},
				"b2": {{Asset: "DAI", WeightBps: 10000}},
			},
		},
		{
			name: "single basket",
			rows: []basketWeight{{"b1", "USDC", 10000, ""}},
			want: map[string][]Weight{"b1": {{Asset: "USDC", WeightBps: 10000}}},
		},
		{name: "no rows", rows: nil, want: map[string][]Weight{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := groupWeights(tt.rows)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d baskets, want %d", len(got), len(tt.want))
			}
			for id, want := range tt.want {
				gw := got[id]
				if len(gw) != len(want) {
					t.Fatalf("basket %s: got %d weights, want %d", id, len(gw), len(want))
				}
				for i := range want {
					if gw[i] != want[i] {
						t.Errorf("basket %s weight %d = %+v, want %+v", id, i, gw[i], want[i])
					}
				}
			}
		})
	}
}

// Grouped weights must still satisfy the basket invariant.
func TestGroupWeightsPreservesTheBpsInvariant(t *testing.T) {
	got := groupWeights([]basketWeight{
		{"b1", "USDC", 6000, ""}, {"b1", "WETH", 4000, ""},
	})
	if err := ValidateWeights(got["b1"]); err != nil {
		t.Errorf("grouped weights failed validation: %v", err)
	}
}
