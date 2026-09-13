Developer feedback: integrating Uniswap v3 on Base

From the sweem team, ETHOnline 2026.
Repo: https://github.com/snehendu098/sweem-basket
Written during the integration, not reconstructed afterwards.


1. Summary

We added a Uniswap v3 swap leg to a Rust service that signs from users' embedded wallets. A user deposits USDC; if the asset they picked is not USDC, we buy it on Uniswap and then supply it to a lending venue, or leave it in their wallet.

The headline finding is that the API was easier than we expected and the integration was more dangerous than we expected, and those two facts are related. The obvious, well typed, fully tested implementation of "let a user buy wstETH with USDC" loses most of the user's money, and nothing in the contract interface, the SDK or the docs warns you. We only avoided it because we measured price impact at two trade sizes before trusting any path.

Everything below is ordered by how much we think it would help the next integrator, not by how much it annoyed us.


2. What we built, for context

Contracts used, Base mainnet (8453):
  SwapRouter02  0x2626664c2603336E57B271c5C0b26F421741e481
  QuoterV2      0x3d4e44Eb1374240CE5F1B871ab261CD16335B76a

Base Sepolia (84532):
  SwapRouter02  0x94cC0AaC535CCDB3C01d6787D6413C739ae12bc4
  QuoterV2      0xC5290058841028F1614F3A6F0F5816cAd0df5E27

Integration surface: exactInputSingle, exactInput, quoteExactInputSingle, quoteExactInput. Rust, using alloy's sol macro for the bindings. No SDK, no routing API, no Permit2. Quotes go over plain eth_call through the same tiny JSON RPC client we already used for receipt polling.

Scale: a static allowlist of 40 paths covering 20 pairs, every one chosen by measurement. Both directions exist for every pair, because a position you can enter and cannot exit is worse than no position.


3. What was genuinely easy, and worth saying

3.1 SwapRouter02 needs nothing special.

We budgeted time for Permit2, a deadline parameter, or a multicall wrapper, because most of what we read implied at least one of the three would be required. None were. approve(router, amount) followed by exactInputSingle from a plain EOA works. Our existing approve then call pipeline, written for Aave and Compound, accepted the router unchanged.

That is a real compliment and we want it recorded as one: the integration cost of the swap was lower than the integration cost of the lending venues sitting next to it in the same code path.

3.2 Simulating before shipping is trivial.

eth_call with balance and allowance state overrides let us run a complete swap against live mainnet state, from an address holding nothing, before writing a line of production code. We never sent a transaction to build this feature.

The failure modes are legible too. Without the allowance override the call reverts with STF, which told us immediately that the router reaches a plain transferFrom and that no Permit2 path was involved. That single revert string answered a question we had budgeted half a day for.

3.3 Fee tiers as uint24 are pleasant.

Packing 500 into three bytes reads as fussy until you write the multi hop path encoder and realise it saves you nothing and costs you nothing. Fine as is.


4. Issues, in priority order

4.1 The direct pools are a trap, and nothing warns you.

Severity: high. This is the one that ships a production incident.

What happened. The obvious implementation of "let a user buy wstETH with USDC" is a single hop USDC to wstETH swap. The pool exists. It quotes. It has a fee tier you can look up in any explorer. It will also destroy the order.

Measured on Base mainnet, price impact against the Chainlink reference at two sizes:

  USDC to wstETH, direct, fee 500      100 USD: -3.23%     1000 USD: -78.4%
  USDC to cbETH, direct, fee 3000      100 USD: -1.79%     1000 USD: -15.5%
  USDC-500-WETH-100-wstETH             100 USD: -0.0003%   1000 USD: -0.0033%
  USDC-500-WETH-500-cbETH              100 USD: -0.0007%   1000 USD: -0.0073%

A minus 78 percent fill at one thousand dollars is not an edge case. It is the normal outcome of the most obvious code you could write.

Why it matters. Nothing in the contract interface distinguishes a deep pool from a nearly empty one at the same fee tier. Both return a quote. Both execute. The only way to find out is to quote several candidate routes and compare, and the integrator has to already suspect there is something to compare. Every integrator must independently rediscover that liquid staking tokens on Base route through WETH, and the ones who skip that step ship a function that is well typed, fully tested, code reviewed and catastrophic.

This is the single largest gap between "the API is easy" and "the integration is correct". The router is a mechanism rather than a policy, which is defensible, but the consequence lands on users rather than on integrators.

What we did. We hardcoded a static path allowlist rather than integrating a routing API. Four pairs initially, twenty now, paths chosen by measurement, exits measured separately from entries. For a deposit product with a fixed asset menu this is strictly better than a router: it is auditable, it has no external dependency, and a path we did not measure cannot be used. It would not scale to arbitrary pairs.

We also refuse assets rather than route them badly. tBTC is deliberately absent: its only v3 pools on Base hold roughly one hundred dollars and quote minus 14.7 percent at 100 USD and minus 91 percent at 1000 USD, so it stays unreachable rather than reachable at a loss. LINK is absent for the same reason, failing on the exit leg at minus 0.870 percent against the oracle even though its entry quoted better than minus 0.4 percent. That asymmetry is itself a trap: a buy side only measurement pass makes several assets look fine.

Suggested fix. Publish canonical, measured paths per chain somewhere machine readable. Even an informational artifact saying "on Base, these pairs should route through WETH" would have saved us a day and will save someone else real user funds. This does not need to be a routing API or a guarantee. A JSON file with a last updated date would do.

4.2 exactInput calldata is not exactInputSingle calldata with a path substituted.

Severity: medium. Silent until it is not.

What happened. ExactInputSingleParams is fully static, so it encodes inline: the first calldata word after the selector is tokenIn. ExactInputParams contains a bytes path, which makes the struct dynamic, so its calldata gains a leading offset word and everything shifts by 32 bytes.

Why it matters. This is correct ABI behaviour and completely unremarkable once someone says it out loud. It is also invisible in the docs, because the two functions are presented side by side as though they differ only in whether you pass a path or a token pair. If you hand roll the encoder, and someone will, because the single hop version is so easy to hand roll, the multi hop version fails in a way that looks like a bad path rather than a bad offset. You will go and re check your fee tiers and your token ordering first.

What we did. We used alloy's sol macro, which gets it right, and then wrote a test asserting that the first word after the selector is 0x20 for the multi hop call and is tokenIn for the single hop call, specifically so nobody later simplifies the encoder and reintroduces it.

Suggested fix. One sentence in the exactInput docs: "exactInput calldata is not exactInputSingle calldata with a path substituted; ExactInputParams is dynamic and carries a leading offset word."

4.3 The QuoterV2 non view warning reads worse than the reality deserves.

Severity: medium. Costs adoption rather than correctness.

What happened. The quoter is documented with a note that it is not a view function and should not be called on chain, because it executes the swap and reverts, recovering the result from the revert data. That is a clever design and the sentence explaining it is accurate.

Why it matters. On a first read it is discouraging in a way the reality does not justify. "Do not call this on chain" plus "this reverts internally" together suggest something fragile, something that needs a special client, or something you should replace with an off chain pricing API. We nearly did exactly that, which would have added an API key, a rate limit and a staleness problem to a system that needed none of them.

The actual situation is that eth_call against QuoterV2 is completely ordinary. No API key, no SDK, no special node, no try/catch, and a normal ABI encoded tuple comes back. For us it meant the quote went through the same fifteen line JSON RPC client we already had, which is the difference between "add a dependency and a secret" and "add fifteen lines".

Suggested fix. Reword to say plainly that calling QuoterV2 over eth_call from an ordinary off chain client is the intended and supported usage, and that the non view warning is about calling it from another contract. As currently written the warning reads as though it applies to everyone.

4.4 QuoteExactInputSingleParams transposes two fields against ExactInputSingleParams.

Severity: medium. Fails silently by design of the type system.

What happened. The quoter orders its fields tokenIn, tokenOut, amountIn, fee, sqrtPriceLimitX96. The router orders them tokenIn, tokenOut, fee, recipient, and so on. amountIn and fee are swapped between the two.

Why it matters. Both are "the same struct" in the integrator's head and different shapes on the wire. Because uint24 fee and uint256 amountIn both encode to a single 32 byte word, transposing them produces a valid call with absurd arguments rather than a decode error. You get a quote back. It is just wrong.

What we did. We pinned all four selectors in tests: exactInputSingle 0x04e45aaf, exactInput 0xb858183f, quoteExactInputSingle 0xc6a5026a, quoteExactInput 0xcdca1753. But selectors do not protect field order, and we want to be explicit that our test suite would not have caught this. Only reading the ABI carefully did.

Suggested fix. A callout in the quoter docs noting the field order differs from the router's equivalent struct, since the two are almost always written in the same sitting.

4.5 sqrtPriceLimitX96 is a footgun with a safe default.

Severity: low, given the default is safe, but the failure is silent.

What happened. Passing a non zero limit silently partial fills. You get fewer tokens than you asked for and no error. amountOutMinimum is the parameter that actually bounds the trade.

Why it matters. The docs do say this, but in a sentence that reads like a tuning option rather than a hazard. "Silently returns less than requested" is an outcome that deserves stronger framing than it currently gets.

What we did. We set the limit to 0 everywhere and wrote a test asserting it stays that way.

Suggested fix. Flag it as a hazard rather than a knob, and state directly that amountOutMinimum is the only parameter that bounds the trade.


5. What we shipped as a result

A static path allowlist, executor/swaps.json: 40 paths, 20 pairs, both directions for every pair, each chosen by measured impact rather than by fee tier. Not caller supplied, so an unlisted pair is refused rather than routed.

A hard rule that a missing quote fails the leg. We never submit a swap with amountOutMinimum of 0. The minimum is always the live quoted amount reduced by the slippage bound, and the caller's requested slippage is clamped server side to between 10 and 300 basis points with a 50 basis point default, so a caller cannot widen its own loss bound. An unreachable quoter fails the leg rather than submitting an unbounded trade.

A swap leg sequenced as approve router, swap, approve venue, supply, with the user's own wallet as recipient throughout. The output of a swap never touches a contract we control.

A test asserting that every entry path has a matching exit whose tokens and fees are exactly the entry reversed, so an asset cannot become enterable and not exitable through an incomplete allowlist edit.


6. What we would ask for, in priority order

6.1 Canonical, measured paths per chain, published somewhere machine readable. Even an informational "these pairs should route through WETH on Base" file. This is by far the highest value item and it prevents user fund loss rather than developer annoyance.

6.2 Price impact in the quoter response, or a documented single call way to obtain it. We currently compute it by quoting two sizes and comparing, which works and is obviously a workaround. Given that item 4.1 is only discoverable by measuring impact, making impact a first class output would close the gap at the source.

6.3 A line in the exactInput docs noting the calldata is not exactInputSingle plus a path.

6.4 Rewording the QuoterV2 non view note to say plainly that eth_call from an ordinary client is the supported path.

6.5 A callout on the quoter and router struct field order difference.


7. Appendix: measured impact, Base mainnet

Entries at 1000 USD, quoted live against QuoterV2 and compared to the Chainlink reference:
  DAI      -0.050%   direct, fee 100
  USDT     +0.030%   direct, fee 100
  USDe     -0.028%   direct, fee 500
  WBTC     -0.108%   USDC-500-cbBTC-100
  VIRTUAL  -0.477%   USDC-500-WETH-500

Exits at 1000 USD:
  WETH     -0.0018%
  cbBTC    +0.0025%
  wstETH   -0.0034%
  cbETH    -0.0073%
  USDbC    -0.0012%
  USDS     -0.0085%
  GHO      -0.0055%
  rETH     -0.0113%
  EURC     -0.0289%
  AERO     -0.0329%
  weETH    -0.0506%
  DAI      -0.019%
  USDT     -0.050%
  USDe     -0.073%
  WBTC     -0.017%
  VIRTUAL  +0.018%

Measured and refused, so they are unreachable rather than reachable at a loss:
  tBTC     -85% to -91%
  LBTC     -99.9%
  ezETH    -47.7%
  sUSDS    -100%
  LINK     -0.870% on the exit leg against the oracle
  AERO     -0.539% on v3 specifically, since its depth is on Aerodrome
  cbXRP, DEGEN, COMP, WELL, MOG all worse than -1%

Uniswap v2 and v4 on Base were measured for every candidate and hold no usable depth for these pairs. v4 unlocks zero tokens.

A note on method, since it changed our conclusions once. An early pass measured entries only, and four assets appeared to clear that later failed on the exit leg. AAVE, MORPHO and VVV were subsequently re admitted after measuring symmetric round trips at 1000 USD, at minus 0.817, minus 0.873 and minus 0.754 percent respectively; the earlier one sided rejection turned out to be oracle versus pool skew rather than real depth. LINK genuinely fails. Anyone measuring impact should measure the round trip, not the buy.
