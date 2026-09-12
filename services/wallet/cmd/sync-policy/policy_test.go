package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// fixture mirrors the shape of executor/venues.json + executor/swaps.json with
// one venue per kind, deliberately mixed-case and duplicated so the tests can
// see normalization happen. No network, no files.
func fixture() (venueFile, swapFile) {
	v := venueFile{Venues: []venue{
		{ID: "v1", Kind: "erc4626", ChainID: 8453, Target: "0xAAAa000000000000000000000000000000000001", Asset: "0xC0C0000000000000000000000000000000000001"},
		{ID: "v2", Kind: "aave_v3", ChainID: 8453, Target: "0xBBbB000000000000000000000000000000000002", Asset: "0xC0C0000000000000000000000000000000000002"},
		// Same Aave pool as v2 with a different asset: the target must dedupe.
		{ID: "v3", Kind: "aave_v3", ChainID: 84532, Target: "0xbbbb000000000000000000000000000000000002", Asset: "0xC0C0000000000000000000000000000000000003"},
		{ID: "v4", Kind: "compound_v3", ChainID: 8453, Target: "0xCCcc000000000000000000000000000000000004", Asset: "0xC0C0000000000000000000000000000000000002"},
		{ID: "v5", Kind: "ctoken", ChainID: 8453, Target: "0xDDdd000000000000000000000000000000000005", Asset: "0xC0C0000000000000000000000000000000000001"},
		// A hold venue: target == asset, and the executor never calls it.
		{ID: "v6", Kind: "hold", ChainID: 8453, Target: "0xEEee000000000000000000000000000000000006", Asset: "0xEEee000000000000000000000000000000000006"},
	}}
	s := swapFile{Paths: []swapPath{
		{ChainID: 8453, Router: "0xR0uter00000000000000000000000000000000ff", TokenIn: "0xEEee000000000000000000000000000000000006"},
		{ChainID: 8453, Router: "0xR0uter00000000000000000000000000000000ff", TokenIn: "0xC0C0000000000000000000000000000000000009"},
	}}
	return v, s
}

func build(t *testing.T) Policy {
	t.Helper()
	v, s := fixture()
	p, err := Build("test", "quorum-1", v, s)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return p
}

// cond finds the one condition on the named field in the first rule whose name
// starts with prefix. Rules are duplicated per method; the conditions are
// identical, so the first is representative.
func cond(t *testing.T, p Policy, prefix, field string) Condition {
	t.Helper()
	for _, r := range p.Rules {
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		for _, c := range r.Conditions {
			if c.Field == field {
				return c
			}
		}
		t.Fatalf("rule %q has no condition on %q", r.Name, field)
	}
	t.Fatalf("no rule named %q*", prefix)
	return Condition{}
}

func values(t *testing.T, c Condition) []string {
	t.Helper()
	vs, ok := c.Value.([]string)
	if !ok {
		t.Fatalf("condition on %q has value %#v, want []string", c.Field, c.Value)
	}
	return vs
}

func TestBuildTargetsAndSpenders(t *testing.T) {
	p := build(t)

	tests := []struct {
		name   string
		prefix string
		field  string
		want   []string
	}{
		{
			name:   "erc4626 targets",
			prefix: "ERC-4626",
			field:  "to",
			want:   []string{"0xaaaa000000000000000000000000000000000001"},
		},
		{
			// Two venues share one Aave pool across two chains: one entry.
			name:   "aave pool deduped and lowercased",
			prefix: "Aave",
			field:  "to",
			want:   []string{"0xbbbb000000000000000000000000000000000002"},
		},
		{
			name:   "comet targets",
			prefix: "Compound",
			field:  "to",
			want:   []string{"0xcccc000000000000000000000000000000000004"},
		},
		{
			name:   "ctoken targets",
			prefix: "Moonwell",
			field:  "to",
			want:   []string{"0xdddd000000000000000000000000000000000005"},
		},
		{
			name:   "swap router",
			prefix: "Uniswap",
			field:  "to",
			want:   []string{"0xr0uter00000000000000000000000000000000ff"},
		},
		{
			// Every token the executor may approve: venue assets (including the
			// hold token) plus swap inputs.
			name:   "approvable tokens",
			prefix: "ERC-20 approve",
			field:  "to",
			want: []string{
				"0xc0c0000000000000000000000000000000000001",
				"0xc0c0000000000000000000000000000000000002",
				"0xc0c0000000000000000000000000000000000003",
				"0xc0c0000000000000000000000000000000000009",
				"0xeeee000000000000000000000000000000000006",
			},
		},
		{
			// The rule that matters most. Spenders are the callable venue
			// targets plus the router — and nothing else. The hold venue's
			// target is a token, not a contract the executor calls, so it must
			// not be spendable-to.
			name:   "approve spenders",
			prefix: "ERC-20 approve",
			field:  "approve.spender",
			want: []string{
				"0xaaaa000000000000000000000000000000000001",
				"0xbbbb000000000000000000000000000000000002",
				"0xcccc000000000000000000000000000000000004",
				"0xdddd000000000000000000000000000000000005",
				"0xr0uter00000000000000000000000000000000ff",
			},
		},
		{
			name:   "chains",
			prefix: "Aave",
			field:  "chain_id",
			want:   []string{"8453", "84532"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := values(t, cond(t, p, tc.prefix, tc.field))
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHoldVenueGetsNoCallRule(t *testing.T) {
	p := build(t)
	hold := "0xeeee000000000000000000000000000000000006"
	for _, r := range p.Rules {
		if r.Action != "ALLOW" {
			continue
		}
		for _, c := range r.Conditions {
			if c.Field != "to" {
				continue
			}
			// The hold token is a legitimate `to` for approve, never for a
			// deposit/withdraw/swap rule.
			if slices.Contains(values(t, c), hold) && !strings.HasPrefix(r.Name, "ERC-20 approve") {
				t.Errorf("rule %q allows calls to the hold token", r.Name)
			}
		}
	}
}

func TestBuildMethodsAndActions(t *testing.T) {
	p := build(t)
	if p.Version != "1.0" || p.ChainType != "ethereum" || p.OwnerID != "quorum-1" {
		t.Fatalf("policy header wrong: %+v", p)
	}

	// Every rule must exist once per signing method, or a request routed through
	// the other method falls through to the default DENY (or, worse, is
	// unconstrained if a broad rule exists).
	byMethod := map[string]int{}
	for _, r := range p.Rules {
		byMethod[r.Method]++
		if r.Action != "ALLOW" && r.Action != "DENY" {
			t.Errorf("rule %q has action %q", r.Name, r.Action)
		}
	}
	if len(byMethod) != len(methods) {
		t.Fatalf("methods covered: %v, want %v", byMethod, methods)
	}
	for _, m := range methods {
		if byMethod[m] != byMethod[methods[0]] {
			t.Errorf("method %s has %d rules, %s has %d", m, byMethod[m], methods[0], byMethod[methods[0]])
		}
	}

	denies := 0
	for _, r := range p.Rules {
		if r.Action == "DENY" {
			denies++
			c := r.Conditions[0]
			if c.Field != "value" || c.Operator != "gt" || c.Value != "0x0" {
				t.Errorf("unexpected DENY condition: %+v", c)
			}
		}
	}
	if denies != len(methods) {
		t.Errorf("got %d DENY rules, want one per method", denies)
	}
}

func TestCalldataConditionsCarryTheirABI(t *testing.T) {
	p := build(t)
	for _, r := range p.Rules {
		for _, c := range r.Conditions {
			switch c.FieldSource {
			case "ethereum_calldata":
				// Privy cannot decode calldata without an ABI; a condition
				// missing one evaluates false and silently blocks the executor.
				if len(c.ABI) == 0 {
					t.Errorf("rule %q: calldata condition on %q has no abi", r.Name, c.Field)
					continue
				}
				var fns []abiFn
				if err := json.Unmarshal(c.ABI, &fns); err != nil {
					t.Errorf("rule %q: abi is not a function list: %v", r.Name, err)
					continue
				}
				// The allowed function names must all be described by the ABI,
				// otherwise the condition can never match.
				named := map[string]bool{}
				for _, f := range fns {
					named[f.Name] = true
				}
				if c.Field == "function_name" {
					for _, want := range asStrings(c.Value) {
						if !named[want] {
							t.Errorf("rule %q allows %q but the abi does not describe it", r.Name, want)
						}
					}
				}
			case "ethereum_transaction":
				if len(c.ABI) != 0 {
					t.Errorf("rule %q: transaction condition on %q carries an abi", r.Name, c.Field)
				}
			default:
				t.Errorf("rule %q: unexpected field_source %q", r.Name, c.FieldSource)
			}
		}
	}
}

func asStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case string:
		return []string{x}
	}
	return nil
}

func TestBuildRejectsBadAllowlists(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*venueFile)
	}{
		{
			// A policy with no ALLOW rules would brick every wallet it is
			// attached to, so an empty allowlist is an error, not an empty doc.
			name:   "no venues",
			mutate: func(v *venueFile) { v.Venues = nil },
		},
		{
			// A kind this generator does not know means the executor encodes
			// calldata nobody here reviewed. Fail rather than guess or skip.
			name:   "unknown kind",
			mutate: func(v *venueFile) { v.Venues[0].Kind = "morpho_blue" },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, s := fixture()
			tc.mutate(&v)
			if _, err := Build("test", "", v, s); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestCanonicalJSONIsSortedAndMinimal(t *testing.T) {
	// The authorization signature is only valid if our canonicalization matches
	// Privy's. Nested object keys must sort too, not just the top level.
	got, err := canonical(map[string]any{
		"version": 1,
		"method":  "PATCH",
		"url":     "https://api.privy.io/v1/policies/p1",
		"body":    json.RawMessage(`{"rules":[{"name":"r","action":"ALLOW"}],"name":"n"}`),
		"headers": map[string]any{"privy-app-id": "app-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"body":{"name":"n","rules":[{"action":"ALLOW","name":"r"}]},` +
		`"headers":{"privy-app-id":"app-1"},"method":"PATCH",` +
		`"url":"https://api.privy.io/v1/policies/p1","version":1}`
	if string(got) != want {
		t.Errorf("canonical:\n got %s\nwant %s", got, want)
	}
}
