package store

import "testing"

func TestValidateWeights(t *testing.T) {
	tests := []struct {
		name    string
		in      []Weight
		wantErr bool
	}{
		{"single asset full weight", []Weight{{"USDC", 10000}}, false},
		{"even three way", []Weight{{"USDC", 4000}, {"WETH", 3000}, {"CBBTC", 3000}}, false},
		{"empty", nil, true},
		{"sum under", []Weight{{"USDC", 5000}}, true},
		{"sum over", []Weight{{"USDC", 6000}, {"WETH", 5000}}, true},
		{"duplicate asset", []Weight{{"USDC", 5000}, {"USDC", 5000}}, true},
		{"zero weight", []Weight{{"USDC", 10000}, {"WETH", 0}}, true},
		{"negative weight", []Weight{{"USDC", 11000}, {"WETH", -1000}}, true},
		{"empty asset name", []Weight{{"", 10000}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWeights(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateWeights(%v) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
		})
	}
}
