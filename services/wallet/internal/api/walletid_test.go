package api

import (
	"strings"
	"testing"
)

func TestValidPrivyWalletID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		ok   bool
		// wantAddressHint asserts the message names the address confusion,
		// which is the entire reason this check exists.
		wantAddressHint bool
	}{
		{
			name: "empty is accepted; Privy has no wallet ID until delegation",
			id:   "", ok: true,
		},
		{
			name: "a real 24-char id",
			id:   "dxvzlpuqjfr6iclupqssmo4a", ok: true,
		},
		{
			name: "all digits is still valid",
			id:   "123456789012345678901234", ok: true,
		},
		{
			name: "all letters is still valid",
			id:   "abcdefghijklmnopqrstuvwx", ok: true,
		},
		{
			name:            "a wallet address is rejected and named as such",
			id:              "0xA36aF77615c89B10FA0fF2d52324b9eac9dd9460",
			wantAddressHint: true,
		},
		{
			name:            "a lowercase address too",
			id:              "0xa36af77615c89b10fa0ff2d52324b9eac9dd9460",
			wantAddressHint: true,
		},
		{
			name:            "an uppercase 0X prefix is still an address",
			id:              "0XA36AF77615C89B10FA0FF2D52324B9EAC9DD9460",
			wantAddressHint: true,
		},
		{
			// Right length, but a 0x prefix means someone truncated an address.
			name:            "a 24-char string that starts with 0x",
			id:              "0xa36af77615c89b10fa0ff2",
			wantAddressHint: true,
		},
		{name: "too short", id: "dxvzlpuqjfr6iclupqssmo4"},
		{name: "too long", id: "dxvzlpuqjfr6iclupqssmo4ab"},
		{name: "uppercase is rejected", id: "DXVZLPUQJFR6ICLUPQSSMO4A"},
		{name: "mixed case is rejected", id: "dxvzlpuqjfr6iclupqssmo4A"},
		{name: "a hyphen is rejected", id: "dxvzlpuqjfr6iclupqssmo-a"},
		{name: "an underscore is rejected", id: "dxvzlpuqjfr6iclupqssmo_a"},
		{name: "a space is rejected", id: "dxvzlpuqjfr6iclupqssmo a"},
		{name: "a did is rejected", id: "did:privy:abc123def456ghi7"},
		{name: "non-ascii is rejected", id: "dxvzlpuqjfr6iclupqssmo4é"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := validPrivyWalletID(tt.id)
			if tt.ok {
				if msg != "" {
					t.Fatalf("rejected a valid id: %s", msg)
				}
				return
			}
			if msg == "" {
				t.Fatal("accepted an invalid id")
			}
			if !strings.Contains(msg, "privy_wallet_id") {
				t.Errorf("message does not name the field: %q", msg)
			}
			if got := strings.Contains(msg, "wallet_address"); got != tt.wantAddressHint {
				t.Errorf("address hint = %v, want %v; message: %q", got, tt.wantAddressHint, msg)
			}
		})
	}
}
