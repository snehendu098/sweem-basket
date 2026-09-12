package chains

import "testing"

func TestIDAndLabelRoundTrip(t *testing.T) {
	for _, id := range Supported() {
		l, ok := Label(id)
		if !ok {
			t.Fatalf("no label for supported chain %d", id)
		}
		if got, ok := ID(l); !ok || got != id {
			t.Fatalf("round trip %d -> %q -> %d/%v", id, l, got, ok)
		}
	}
}

func TestIDIsCaseInsensitiveButNotFuzzy(t *testing.T) {
	if id, _ := ID("Base"); id != BaseMainnet {
		t.Fatalf(`ID("Base") = %d, want mainnet`, id)
	}
	if id, _ := ID(" Base-Sepolia "); id != BaseSepolia {
		t.Fatalf("ID with padding did not resolve to sepolia")
	}
	// The failure to design against: a near-miss label must not resolve at all
	// rather than land on the wrong network.
	for _, bad := range []string{"", "sepolia", "base sepolia", "basesepolia", "base-mainnet", "ethereum"} {
		if _, ok := ID(bad); ok {
			t.Fatalf("ID(%q) resolved; unknown labels must fail loudly", bad)
		}
	}
}

func TestNormalize(t *testing.T) {
	if got, _ := Normalize("BASE"); got != LabelBaseMainnet {
		t.Fatalf("Normalize(BASE) = %q", got)
	}
	if _, ok := Normalize("nope"); ok {
		t.Fatal("Normalize accepted an unknown label")
	}
}
