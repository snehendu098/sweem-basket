# Developer feedback: integrating Uniswap v3 on Base

We added a Uniswap v3 swap leg to a Rust executor that signs from users' embedded
wallets. The user deposits USDC; if the asset they picked is not USDC, we buy it
on Uniswap and then supply it to a lending venue, or just leave it in their
wallet. Everything below is friction we actually hit, in the order we hit it.

## What was genuinely easy

**SwapRouter02 needs nothing special.** We expected to have to integrate Permit2,
a deadline, or a `multicall` wrapper, because most of what we read implied one of
the three. None of it was needed. `approve(router, amount)` followed by
`exactInputSingle` from a cold EOA works, and our whole existing
approve-then-call pipeline — written for Aave and Compound — took the router
unchanged. That is a real compliment: the integration cost of the swap was
smaller than the integration cost of the lending venues it sits next to.

**Simulating before shipping is trivial.** `eth_call` with balance and allowance
state overrides let us run a complete swap from an address holding nothing, on
mainnet state, before writing a line of production code. The failure modes are
legible too: without the allowance override the call reverts with `STF`, which
told us immediately that the router reaches a plain `transferFrom` and that no
Permit2 path was involved. We never sent a transaction to build this feature.

**Fee tiers as uint24 are pleasant.** Packing `500` into three bytes is the kind
of encoding decision that reads as fussy until you write the multi-hop path and
realise it saves you nothing but costs you nothing either.

## What was not

### The direct pools are a trap, and nothing warns you

The obvious implementation of "let a user buy wstETH with USDC" is a single-hop
USDC→wstETH swap. The pool exists. It quotes. It has a fee tier you can look up.
It will also destroy the order. Measured on Base mainnet:

| path | $100 | $1,000 |
|---|---|---|
| USDC→wstETH direct (500) | −3.23% | **−78.4%** |
| USDC→cbETH direct (3000) | −1.79% | −15.5% |
| USDC-500-WETH-100-wstETH | −0.0003% | −0.0033% |
| USDC-500-WETH-500-cbETH | −0.0007% | −0.0073% |

A −78% fill at one thousand dollars is not an edge case, it is the *normal*
outcome of the most obvious code you could write. Nothing in the contract
interface distinguishes a deep pool from a nearly empty one with the same fee
tier — you only find out by quoting both and comparing. We only found out because
we measured impact at two sizes before trusting anything.

This is the single biggest gap between "the API is easy" and "the integration is
correct". The router is a mechanism, not a policy, which is defensible — but the
consequence is that every integrator must independently rediscover that the
liquid-staking tokens route through WETH, and the ones who skip that step ship a
working, well-typed, fully-tested function that loses most of the user's money.
A published "these are the canonical paths on this chain" artifact, even an
informational one, would have saved us a day and would save someone else a
production incident.

We ended up hardcoding a static path allowlist (`executor/swaps.json`) rather
than integrating a routing API — four pairs, paths chosen by measurement. For a
deposit product with a fixed asset menu this is strictly better than a router: it
is auditable, it has no external dependency, and a path we did not measure cannot
be used. It would not scale to arbitrary pairs.

### `ExactInputParams` has an ABI shape that `ExactInputSingleParams` does not

`ExactInputSingleParams` is fully static, so it encodes inline: the first calldata
word after the selector is `tokenIn`. `ExactInputParams` contains a `bytes path`,
which makes the struct dynamic, so its calldata gains a leading offset word and
everything shifts by 32 bytes.

This is correct ABI behaviour and completely unremarkable once you say it out
loud. It is also invisible in the docs, because the two functions are documented
side by side as though they differ only in whether you pass a path or a pair. If
you hand-roll the encoder — and someone will, because the single-hop one is so
easy to hand-roll — the multi-hop version fails in a way that looks like a bad
path rather than a bad offset. We used `alloy`'s `sol!` macro, which gets it
right, and then wrote a test asserting the first word is `0x20` specifically so
that nobody "simplifies" it later.

Worth a sentence in the docs: *`exactInput` calldata is not `exactInputSingle`
calldata with a path substituted.*

### QuoterV2 being non-`view` reads badly in the docs

The quoter is documented with the note that it is not a `view` function and
should not be called on-chain, because it executes the swap and reverts,
recovering the result from the revert data. That is a clever design, and the
sentence explaining it is accurate. It is also, on a first read, *discouraging* in
a way the reality does not deserve — "do not call this on-chain" and "this
reverts internally" together suggest something fragile, something that needs a
special client, or something you should replace with an off-chain pricing API.

The actual situation is that `eth_call` against QuoterV2 is completely ordinary.
It needs no API key, no SDK, no special node, no `try/catch`, and it returns a
normal ABI-encoded tuple. For us it meant the quote could go through the same
tiny JSON-RPC client we already used for receipt polling, which is the difference
between "add a dependency and a secret" and "add fifteen lines".

What the docs could say, and do not: **calling QuoterV2 over `eth_call` from a
normal client is the intended and supported usage; the non-`view` warning is
about calling it from another contract.** As written, the warning reads as though
it applies to everyone. We nearly reached for an off-chain price API because of
it, which would have added a key, a rate limit, and a staleness problem to a
system that needed none of them.

One smaller thing: `QuoteExactInputSingleParams` orders its fields
`(tokenIn, tokenOut, amountIn, fee, sqrtPriceLimitX96)`, while the router's
`ExactInputSingleParams` orders them `(tokenIn, tokenOut, fee, recipient, ...)`.
`amountIn` and `fee` are swapped between the two. Both are structs of the same
shape in your head and different shapes on the wire, and since `uint24 fee` and
`uint256 amountIn` both encode to a 32-byte word, transposing them produces a
valid call with absurd arguments rather than a decode error. We pinned all four
selectors in tests, but selectors do not protect field order — only reading the
ABI carefully does.

### `sqrtPriceLimitX96` is a footgun with a safe default

Passing a non-zero limit silently partial-fills: you get fewer tokens than you
asked for and no error. `amountOutMinimum` is the parameter that actually bounds
the trade. We set the limit to `0` everywhere and wrote a test asserting it stays
that way. The docs do say this, but they say it in a sentence that reads like a
tuning option rather than a hazard, and "silently returns less than requested" is
an outcome that deserves to be flagged harder than it is.

## What we'd ask for

1. Canonical, measured paths per chain, published somewhere machine-readable —
   even just "these pairs should route through WETH on Base".
2. A line in the `exactInput` docs noting the calldata is not merely
   `exactInputSingle` plus a path.
3. Rewording the QuoterV2 non-`view` note to say plainly that `eth_call` from an
   ordinary client is the supported path.
4. Price impact in the quoter response, or a documented way to get it in one
   call. We compute it by quoting two sizes and comparing, which works and is
   obviously a workaround.

## What we shipped

- `executor/swaps.json` — static path allowlist, four pairs, paths chosen by
  measured impact. Not caller-supplied: an unlisted pair is refused.
- `executor/src/swaps.rs` — `sol!` bindings, path packing, QuoterV2 quoting,
  slippage clamped server-side to 10–300bps with a 50bps default.
- A swap leg that turns a deposit into `approve(router) → swap → approve(venue)
  → supply`, with the user's own wallet as `recipient` throughout. The output of
  a swap never touches a contract we control.
- A hard rule that a missing quote fails the leg. We never submit a swap with
  `amountOutMinimum: 0`.
