package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

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

type Policy struct {
	ID        string `json:"id,omitempty"`
	Version   string `json:"version"`
	Name      string `json:"name"`
	ChainType string `json:"chain_type"`
	Rules     []Rule `json:"rules"`
	OwnerID   string `json:"owner_id,omitempty"`
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

var methods = []string{"eth_sendTransaction", "eth_signTransaction"}

var methodTag = map[string]string{"eth_sendTransaction": "send", "eth_signTransaction": "sign"}

type abiArg struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Components []abiArg `json:"components,omitempty"`
}

type abiFn struct {
	Name            string   `json:"name"`
	Type            string   `json:"type"`
	Inputs          []abiArg `json:"inputs"`
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
		panic(err)
	}
	return b
}

type venueKind struct {
	kind  string
	label string
	fns   []abiFn
}

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

func Build(name, ownerID string, v venueFile, s swapFile) (Policy, error) {
	if len(v.Venues) == 0 {
		return Policy{}, fmt.Errorf("no venues: refusing to build a policy that allows nothing")
	}

	targets := map[string]*set{}
	chains := &set{}
	assets := &set{}
	spenders := &set{}
	routers := &set{}

	known := map[string]bool{}
	for _, k := range venueKinds {
		known[k.kind] = true
	}

	for _, x := range v.Venues {
		if !known[x.Kind] {
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
				Name:       name + " (" + methodTag[m] + ")",
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

	approveAbi := abiOf(approveFn)
	add("ERC-20 approve to an allowlisted spender",
		chainCond,
		Condition{FieldSource: "ethereum_transaction", Field: "to", Operator: "in", Value: assets.list()},
		Condition{FieldSource: "ethereum_calldata", Field: "function_name", ABI: approveAbi, Operator: "eq", Value: "approve"},
		Condition{FieldSource: "ethereum_calldata", Field: "approve.spender", ABI: approveAbi, Operator: "in", Value: spenders.list()},
	)

	for _, m := range methods {
		rules = append(rules, Rule{
			Name:   "Deny any native value transfer (" + methodTag[m] + ")",
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
