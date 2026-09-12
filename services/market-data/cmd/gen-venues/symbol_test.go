package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/source"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

// The executor compares a venue's symbol with `!=`, against the basket asset
// and against the swap allowlist, and all three of those spell a token the way
// the token spells itself: swaps.json says "wstETH".
//
// The casing has broken this path twice, in two different places — first
// venues.json against swaps.json, then venues.json against the basket asset —
// so this test pins the whole chain rather than one link of it:
//
//	swaps.json symbol  ==  ResolveAsset(that token's address)  ==  venue.Asset
//	                   ==  on-chain symbol()  ==  venues.json symbol
//
// Uppercase stays inside market-data as a lookup key and nowhere else.
func TestEmittedSymbolMatchesSwapAllowlist(t *testing.T) {
	for _, tok := range swapTokens(t) {
		t.Run(tok.Symbol, func(t *testing.T) {
			// Link 1: market-data resolves that token to that exact spelling,
			// so the asset field the client turns into a basket weight — and
			// the wallet turns into RouteRequest.asset — already agrees.
			asset := source.ResolveAsset([]string{tok.Address}, "")
			if asset != tok.Symbol {
				t.Fatalf("ResolveAsset(%s) = %q, but swaps.json calls it %q; the basket asset would not match the venue",
					tok.Address, asset, tok.Symbol)
			}

			// Link 2: the generator emits the chain's symbol() verbatim, and a
			// venue carrying that asset lands on the same string.
			rpc := &stubRPC{ret: map[string]string{
				strings.ToLower(tok.Address) + source.SelSymbol:   abiString(tok.Symbol),
				strings.ToLower(tok.Address) + source.SelDecimals: "0x" + strings.Repeat("0", 62) + "12",
			}}
			v := venue.Venue{
				ID:      venue.MakeID(chains.LabelBaseMainnet, source.ProtocolHold, strings.ToLower(tok.Address)),
				Chain:   chains.LabelBaseMainnet,
				Project: source.ProtocolHold,
				PoolID:  strings.ToLower(tok.Address),
				Asset:   asset,
			}
			c := source.Chain{Label: chains.LabelBaseMainnet, ID: chains.BaseMainnet}
			e, err := verify(context.Background(), source.NewCaller(rpc), c, v, "hold")
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if e.Symbol != tok.Symbol {
				t.Errorf("emitted symbol %q, want the on-chain %q that swaps.json matches on", e.Symbol, tok.Symbol)
			}
			if e.Symbol != v.Asset {
				t.Errorf("venues.json would carry symbol %q while the venue's asset is %q; the executor compares them with !=", e.Symbol, v.Asset)
			}
		})
	}
}

// swapToken is one token the swap allowlist names, with the address it names it
// at — so the test can ask market-data what it calls that same address.
type swapToken struct {
	Symbol  string
	Address string
}

// swapTokens reads every token the executor's swap allowlist names. That file
// is the other half of the contract, so the test reads it rather than restating
// its contents.
func swapTokens(t *testing.T) []swapToken {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "executor", "swaps.json"))
	if err != nil {
		t.Fatalf("read swaps.json: %v", err)
	}
	var f struct {
		Paths []struct {
			From    string `json:"from"`
			To      string `json:"to"`
			TokenIn string `json:"token_in"`
			Hops    []struct {
				Token string `json:"token"`
			} `json:"hops"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatalf("parse swaps.json: %v", err)
	}
	seen := map[string]bool{}
	out := []swapToken{}
	add := func(symbol, address string) {
		if symbol == "" || address == "" || seen[symbol] {
			return
		}
		seen[symbol] = true
		out = append(out, swapToken{symbol, address})
	}
	for _, p := range f.Paths {
		add(p.From, p.TokenIn)
		if n := len(p.Hops); n > 0 {
			// The last hop's token is the path's destination.
			add(p.To, p.Hops[n-1].Token)
		}
	}
	if len(out) == 0 {
		t.Fatal("swaps.json names no tokens; the casing contract cannot be checked")
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

type stubRPC struct{ ret map[string]string }

func (s *stubRPC) Call(_ context.Context, to, data string) (string, error) {
	if v, ok := s.ret[strings.ToLower(to)+strings.ToLower(data)]; ok {
		return v, nil
	}
	return "", errors.New("execution reverted")
}

// abiString encodes a solidity string return.
func abiString(s string) string {
	b := []byte(s)
	padded := make([]byte, ((len(b)/32)+1)*32)
	copy(padded, b)
	return fmt.Sprintf("0x%064x%064x%x", 32, len(b), padded)
}
