package main

// Policy document generation.
//
// The shape of the allowlist that the executor enforces locally
// (`executor/venues.json`, `executor/swaps.json`) is turned into a Privy policy
// so the same constraint is enforced by Privy's enclave, outside our code. Every
// address here is derived from those files; nothing is hand-written.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- allowlists

type venueFile struct {
	Venues []venue `json:"venues"`
}

type venue struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	ChainID int64  `json:"chain_id"`
	Target  string `json:"target"`
	Asset   string `json:"asset"`
}

type swapFile struct {
	Paths []swapPath `json:"paths"`
}

type swapPath struct {
	ChainID int64  `json:"chain_id"`
	Router  string `json:"router"`
	TokenIn string `json:"token_in"`
}

// ------------------------------------------------------------- policy schema
//
// https://docs.privy.io/controls/policies/overview — policy -> rules ->
// conditions. A policy denies by default: any RPC method without a matching
// ALLOW rule is refused, and a DENY always beats an ALLOW.

type Policy struct {
	Version   string `json:"version"`
	Name      string `json:"name"`
	ChainType string `json:"chain_type"`
	Rules     []Rule `json:"rules"`
	// OwnerID is the key quorum whose signature is needed to modify the policy.
	// Omitted on PATCH, where the id is in the URL and the owner is already set.
	OwnerID string `json:"owner_id,omitempty"`
}

type Rule struct {
	Name       string      `json:"name"`
	Method     string      `json:"method"`
	Conditions []Condition `json:"conditions"`
	Action     string      `json:"action"`
}

type Condition struct {
	FieldSource string          `json:"field_source"`
	Field       string          `json:"field"`
	ABI         json.RawMessage `json:"abi,omitempty"`
	Operator    string          `json:"operator"`
	Value       any             `json:"value"`
}

// methods carrying the rules. The executor only uses eth_sendTransaction
// (executor/src/privy.rs), but eth_signTransaction is the same authority minus
// the broadcast, so it gets the identical rules rather than being left to the
// default deny — and it is the one method that can be exercised against a real
// wallet without spending anything, which is how enforcement gets verified.
var methods = []string{"eth_sendTransaction", "eth_signTransaction"}

// ------------------------------------------------------------------- the ABIs

type abiArg struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Components []abiArg `json:"components,omitempty"`
}

type abiFn struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`  // always "function"
	Inputs []abiArg `json:"inputs"`
	// Privy decodes calldata only; outputs never matter, but the field is part
	// of a well-formed ABI entry so it is emitted as an empty list.
	Outputs         []abiArg `json:"outputs"`
	StateMutability string   `json:"stateMutability"`
}

func fn(name string, inputs ...abiArg) abiFn {
	return abiFn{Name: name, Type: "function", Inputs: inputs, Outputs: []abiArg{}, StateMutability: "nonpayable"}
}

func arg(name, typ string) abiArg { return abiArg{Name: name, Type: typ} }

func tuple(name string, components ...abiArg) abiArg {
	return abiArg{Name: name, Type: "tuple", Components: components}
}

func abiOf(fns ...abiFn) json.RawMessage {
	b, err := json.Marshal(fns)
	if err != nil {
		panic(err) // static data; a failure here is a programming error
	}
	return b
}

// venueKind pairs a kind in venues.json with the exact methods the executor
// encodes for it (executor/src/venues.rs `deposit_call` / `withdraw_call`).
type venueKind struct {
	kind  string
	label string
	fns   []abiFn
}

// Order is fixed so the generated document is byte-stable across runs.
var venueKinds = []venueKind{
	{"erc4626", "ERC-4626 vault", []abiFn{
		fn("deposit", arg("assets", "uint256"), arg("receiver", "address")),
		fn("withdraw", arg("assets", "uint256"), arg("receiver", "address"), arg("owner", "address")),
		fn("redeem", arg("shares", "uint256"), arg("receiver", "address"), arg("owner", "address")),
	}},
	{"aave_v3", "Aave v3 pool", []abiFn{
		fn("supply", arg("asset", "address"), arg("amount", "uint256"), arg("onBehalfOf", "address"), arg("referralCode", "uint16")),
		fn("withdraw", arg("asset", "address"), arg("amount", "uint256"), arg("to", "address")),
	}},
	{"compound_v3", "Compound v3 comet", []abiFn{
		fn("supplyTo", arg("dst", "address"), arg("asset", "address"), arg("amount", "uint256")),
		fn("withdrawTo", arg("to", "address"), arg("asset", "address"), arg("amount", "uint256")),
	}},
	{"ctoken", "Moonwell cToken", []abiFn{
		fn("mint", arg("mintAmount", "uint256")),
		fn("redeemUnderlying", arg("redeemAmount", "uint256")),
	}},
	// "hold" is deliberately absent: a hold venue has no contract to call, and
	// deposit_call/withdraw_call refuse it. Its token is reachable only as a
	// swap input or an approval target, both covered by other rules.
	{"hold", "", nil},
}

var swapFns = []abiFn{
	{Name: "exactInputSingle", Type: "function", StateMutability: "payable", Outputs: []abiArg{}, Inputs: []abiArg{
		tuple("params",
			arg("tokenIn", "address"), arg("tokenOut", "address"), arg("fee", "uint24"),
			arg("recipient", "address"), arg("amountIn", "uint256"),
			arg("amountOutMinimum", "uint256"), arg("sqrtPriceLimitX96", "uint160")),
	}},
	{Name: "exactInput", Type: "function", StateMutability: "payable", Outputs: []abiArg{}, Inputs: []abiArg{
		tuple("params",
			arg("path", "bytes"), arg("recipient", "address"),
			arg("amountIn", "uint256"), arg("amountOutMinimum", "uint256")),
	}},
}

var approveFn = fn("approve", arg("spender", "address"), arg("amount", "uint256"))

// --------------------------------------------------------------- the builder

// Build turns the two allowlists into a policy document.
//
// Addresses are lowercased: Privy compares EVM addresses case-insensitively for
// `ethereum_transaction.to` and for address arguments decoded from calldata
// (https://docs.privy.io/controls/policies/condition-sets), so one form is
// enough, and a single form keeps the output diffable.
func Build(name, ownerID string, v venueFile, s swapFile) (Policy, error) {
	if len(v.Venues) == 0 {
		return Policy{}, fmt.Errorf("no venues: refusing to build a policy that allows nothing")
	}

	targets := map[string]*set{}
	chains := &set{}
	assets := &set{}   // ERC-20s the executor may call `approve` on
	spenders := &set{} // who those approvals may name
	routers := &set{}

	known := map[string]bool{}
	for _, k := range venueKinds {
		known[k.kind] = true
	}

	for _, x := range v.Venues {
		if !known[x.Kind] {
			// A new kind means the executor encodes calldata this generator does
			// not describe. Failing is the only safe answer: silently skipping it
			// would produce a policy that blocks a supported venue, and guessing
			// would produce one that allows calldata nobody reviewed.
			return Policy{}, fmt.Errorf("venue %s: unknown kind %q; teach sync-policy this kind before regenerating", x.ID, x.Kind)
		}
		chains.add(strconv.FormatInt(x.ChainID, 10))
		assets.add(x.Asset)
		if x.Kind == "hold" {
			continue
		}
		if targets[x.Kind] == nil {
			targets[x.Kind] = &set{}
		}
		targets[x.Kind].add(x.Target)
		spenders.add(x.Target)
	}

	for _, p := range s.Paths {
		chains.add(strconv.FormatInt(p.ChainID, 10))
		assets.add(p.TokenIn)
		routers.add(p.Router)
		spenders.add(p.Router)
	}

	chainCond := Condition{
		FieldSource: "ethereum_transaction", Field: "chain_id",
		Operator: "in", Value: chains.list(),
	}

	var rules []Rule
	add := func(name string, conds ...Condition) {
		for _, m := range methods {
			rules = append(rules, Rule{
				Name:       name + " [" + m + "]",
				Method:     m,
				Action:     "ALLOW",
				Conditions: conds,
			})
		}
	}

	for _, k := range venueKinds {
		t := targets[k.kind]
		if t == nil || t.empty() {
			continue
		}
		add(k.label+" deposit/withdraw",
			chainCond,
			Condition{FieldSource: "ethereum_transaction", Field: "to", Operator: "in", Value: t.list()},
			Condition{FieldSource: "ethereum_calldata", Field: "function_name", ABI: abiOf(k.fns...), Operator: "in", Value: fnNames(k.fns)},
		)
	}

	if !routers.empty() {
		add("Uniswap SwapRouter02 swap",
			chainCond,
			Condition{FieldSource: "ethereum_transaction", Field: "to", Operator: "in", Value: routers.list()},
			Condition{FieldSource: "ethereum_calldata", Field: "function_name", ABI: abiOf(swapFns...), Operator: "in", Value: fnNames(swapFns)},
		)
	}

	// The highest-value rule in the document. An `approve` with an unconstrained
	// spender is how funds usually leave a wallet, so the spender is pinned to
	// the same allowlist the `to` rules use.
	approveAbi := abiOf(approveFn)
	add("ERC-20 approve to an allowlisted spender",
		chainCond,
		Condition{FieldSource: "ethereum_transaction", Field: "to", Operator: "in", Value: assets.list()},
		Condition{FieldSource: "ethereum_calldata", Field: "function_name", ABI: approveAbi, Operator: "eq", Value: "approve"},
		Condition{FieldSource: "ethereum_calldata", Field: "approve.spender", ABI: approveAbi, Operator: "in", Value: spenders.list()},
	)

	// Every call the executor builds carries value 0x0 (executor/src/privy.rs).
	// A DENY beats any ALLOW, so this closes native-ETH movement across all the
	// rules above without repeating a condition in each of them.
	for _, m := range methods {
		rules = append(rules, Rule{
			Name:   "Deny any native value transfer [" + m + "]",
			Method: m,
			Action: "DENY",
			Conditions: []Condition{{
				FieldSource: "ethereum_transaction", Field: "value", Operator: "gt", Value: "0x0",
			}},
		})
	}

	return Policy{
		Version:   "1.0",
		Name:      name,
		ChainType: "ethereum",
		Rules:     rules,
		OwnerID:   ownerID,
	}, nil
}

func fnNames(fns []abiFn) []string {
	out := make([]string, len(fns))
	for i, f := range fns {
		out[i] = f.Name
	}
	return out
}

// set is a sorted, deduplicated, lowercased string set. Sorting is what makes
// re-running the tool against an unchanged allowlist a no-op diff.
type set struct{ m map[string]struct{} }

func (s *set) add(v string) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return
	}
	if s.m == nil {
		s.m = map[string]struct{}{}
	}
	s.m[v] = struct{}{}
}

func (s *set) empty() bool { return len(s.m) == 0 }

func (s *set) list() []string {
	out := make([]string, 0, len(s.m))
	for v := range s.m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
