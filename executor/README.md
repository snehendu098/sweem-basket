# executor

The only component that signs and submits blockchain transactions. It is called
by the Go wallet service and is never exposed publicly.

This is copy-trading, not a pooled vault: funds stay in each end user's own
Privy embedded wallet. The executor moves them via **Privy delegated actions** —
the user granted the app permission to act on their behalf. No wallet controlled
by this service ever holds user funds.

Two independent limits bound what can happen:

1. **Privy's server-side policy engine** — enforced by Privy, outside this process.
2. **The local venue allowlist** (`venues.json`) — enforced here.
3. **The local swap-path allowlist** (`swaps.json`) — enforced here.

## Endpoints

| method | path | purpose |
|---|---|---|
| `GET`  | `/health` | Privy reachability, venue count, chains served |
| `GET`  | `/venues` | the allowlist, optionally `?chain_id=`. Read-only. The wallet service filters its routing against it so it never proposes a venue that would be refused at submission time. |
| `POST` | `/route` | the only write path |

## Security model

- A swap path absent from `swaps.json` is refused, and a swap is never submitted
  without a live QuoterV2 quote: `amountOutMinimum` is always
  `quoted * (10_000 - slippage_bps) / 10_000`, never 0. The caller's
  `max_slippage_bps` is clamped server-side to 10..=300 (default 50).
- A venue ID absent from `venues.json` is refused. No calldata is ever built for
  an address that did not come from the registry.
- `to` on every transaction comes from the registry, never from the request body.
  The request only names venue IDs.
- Approvals target the **asset** contract; deposits and withdrawals target the
  **venue** contract. Tested (`approve_targets_the_asset_not_the_vault`).
- Withdraw and redeem pay out to the user's own wallet address only
  (`withdraw_pays_out_to_the_owner`).
- Every request must name `chain_id`, and every venue it references must live on
  that chain. A Sepolia venue on a mainnet request (or the reverse) is refused
  before any calldata exists (`venue_from_another_chain_is_refused`).
- A venue id whose chain label disagrees with its `chain_id` makes the process
  refuse to start, so the allowlist cannot be quietly mislabelled.
- A cross-chain rebalance is refused rather than half-executed.
- `amount_usd <= 0` is refused before anything is signed.
- USD → token units is only correct for USD-pegged assets; anything else is
  refused pending a price feed.

## Env vars

| var                 | required | default        |
|---------------------|----------|----------------|
| `PRIVY_APP_ID`      | yes      | —              |
| `PRIVY_APP_SECRET`  | yes      | —              |
| `PRIVY_AUTHORIZATION_PRIVATE_KEY` | no | — (unsigned requests) |
| `EXECUTOR_ADDR`     | no       | `0.0.0.0:8082` |
| `VENUES_PATH`       | no       | `venues.json`  |
| `SWAPS_PATH`        | no       | `swaps.json`   |
| `BASE_RPC_URL_8453`  | no      | `https://mainnet.base.org` |
| `BASE_RPC_URL_84532` | no      | `https://sepolia.base.org` |
| `RECEIPT_TIMEOUT`   | no       | `60` (seconds) |

`.env` then `../.env` are read as a fallback; real environment variables always
win. `PRIVY_APP_SECRET` and `PRIVY_AUTHORIZATION_PRIVATE_KEY` are not in the
repo `.env` and must not be. Neither is ever logged: `Config`'s `Debug` impl
prints `<redacted>` for both.

Missing config, or an allowlist that is unparseable, unreadable **or empty**,
exits non-zero at startup — the service will not run half-configured. An empty
allowlist would start a service that refuses every request, which reads like a
code bug rather than the misconfiguration it is.

## Run

```sh
cargo run                 # from this directory, so venues.json resolves
cargo test
```

## API

### `GET /health`

```json
{ "status": "ok" | "degraded", "privy": "up" | "down", "venues": 4 }
```

Always 200; `degraded` means the Privy API did not answer.

### `POST /route`

```json
{
  "execution_id": "uuid",
  "user_wallet": "0x…",
  "privy_wallet_id": "…",
  "privy_did": "did:privy:…",
  "chain": "Base",
  "asset": "USDC",
  "amount_usd": 100.0,
  "action": "deposit | withdraw | rebalance",
  "from_venue_id": "…",
  "to_venue_id": "…",
  "max_slippage_bps": 50
}
```

`privy_did`, `chain` and `max_slippage_bps` are accepted and ignored today.
Unknown fields never fail a request.

Response (200 on success or `pending`, 400 on a rejected request, 502 on a
Privy failure or an on-chain revert):

```json
{
  "execution_id": "uuid",
  "tx_hash": "0x…",
  "status": "submitted",
  "error": "",
  "steps": [
    { "step": 0, "tx_hash": "0xaa…", "outcome": "confirmed" },
    { "step": 1, "tx_hash": "0xbb…", "outcome": "submitted" }
  ]
}
```

`status`:

| value       | meaning |
|-------------|---------|
| `submitted` | every call went out; all but the last are confirmed on chain |
| `pending`   | a call did not confirm within `RECEIPT_TIMEOUT`. It may still land — **do not retry blindly**, reconcile by hash |
| `failed`    | a call reverted, or Privy refused it. The sequence stopped there |

`tx_hash` is the last hash submitted. `steps` carries every call with its own
hash and outcome (`confirmed` / `reverted` / `pending` / `submitted` / `failed`),
so a three-call rebalance that reverts on step 2 is legible from the response
alone. A request rejected before submission has no `steps`.

Calls per action:

| action      | calls                                              |
|-------------|----------------------------------------------------|
| `deposit`   | `approve(asset→venue)`, then deposit/supply         |
| `withdraw`  | ERC-4626 `withdraw` or Aave `withdraw`, to the user |
| `rebalance` | withdraw from source, approve, deposit into target  |

### Receipt polling

Each call in a sequence depends on the previous one having landed — approve
before deposit, withdraw before redeposit — and Privy returns on *submission*,
not inclusion. So after every call except the last, the executor polls
`eth_getTransactionReceipt` once a second until the receipt appears or
`RECEIPT_TIMEOUT` elapses. A receipt with `status: 0x0` stops the sequence
immediately; the next call is never submitted.

This does **not** go through Privy. `POST /v1/wallets/{id}/rpc` is a signing
API scoped to a single wallet and dispatches signing methods; a receipt lookup
is not wallet-scoped and is not among them. Confirmation therefore comes from
an ordinary JSON-RPC node at `BASE_RPC_URL`, which needs no credentials because
it is read-only.

A node we cannot reach is not evidence a transaction failed, so transport
errors are treated as "not mined yet" and fall through to the `pending`
timeout rather than to `failed`.

Each call is submitted to Privy with `reference_id = "<execution_id>-<step>"`
(truncated to Privy's 64-char limit). That is the idempotency handle: a retry
after a network timeout is reconciled instead of double-spent.

## Privy call

```
POST https://api.privy.io/v1/wallets/{wallet_id}/rpc
Authorization: Basic base64(app_id:app_secret)
privy-app-id: <app id>
privy-authorization-signature: <base64 DER P-256 sig>   # when a key is configured

{"method":"eth_sendTransaction","caip2":"eip155:8453","chain_type":"ethereum",
 "params":{"transaction":{"to":"0x…","data":"0x…","value":"0x0","chain_id":8453}},
 "reference_id":"…"}
```

## Authorization signatures

Wallets owned by an authorization key or a key quorum reject any state-changing
request that does not carry a `privy-authorization-signature` header. Every POST
this service makes to Privy is signed when `PRIVY_AUTHORIZATION_PRIVATE_KEY` is
set. If it is absent the executor logs a warning at startup and sends requests
unsigned — fine for wallets with no key owner, a guaranteed rejection for any
other.

`PRIVY_AUTHORIZATION_PRIVATE_KEY` is the string Privy's dashboard gives you:
`wallet-auth:<base64 PKCS#8 DER P-256 private key>`. A malformed value is a
hard startup failure; an absent one is not.

### Canonical payload

The signed bytes are the RFC 8785 (JCS) canonicalization of:

| field | value |
|---|---|
| `version` | `1` (only version) |
| `method` | the HTTP method — `POST`/`PUT`/`PATCH`/`DELETE`. GETs are never signed. |
| `url` | the **full** URL including scheme and host, no trailing slash |
| `body` | the JSON request body, verbatim |
| `headers` | only `privy-`-prefixed headers: `privy-app-id` (required), plus `privy-idempotency-key` / `privy-request-expiry` **only if actually sent**. Never `Authorization` or `Content-Type`. |

RFC 8785 means: object keys sorted, no whitespace. So for our wallet RPC call
the signed string is literally:

```
{"body":{"caip2":"eip155:8453","chain_type":"ethereum","method":"eth_sendTransaction","params":{...},"reference_id":"..."},"headers":{"privy-app-id":"<app id>"},"method":"POST","url":"https://api.privy.io/v1/wallets/<wallet_id>/rpc","version":1}
```

Two details that are easy to miss and produce an opaque 401:

- **An empty object body is serialized as the empty string `""`, not `{}`.**
  This is not in the prose docs; it is in the SDK
  (`formatRequestForAuthorizationSignature`).
- The signature is **DER**-encoded (not raw `r||s`) and **standard** base64 with
  padding (not base64url).

Sign with ECDSA P-256 over SHA-256 of those bytes, base64 the DER, put it in
`privy-authorization-signature`. For a key quorum, each key signs the same
payload and the signatures go in that one header **comma-separated**.

Sources: Privy docs, [Implementing signing
directly](https://docs.privy.io/controls/authorization-keys/using-owners/sign/direct-implementation)
and [Signing requests with key
quorums](https://docs.privy.io/controls/key-quorum/sign); cross-checked
byte-for-byte against `@privy-io/node@0.34.0`, `src/lib/authorization.ts`.
`matches_privy_sdk_golden_vectors` in `src/auth.rs` pins signatures generated by
that SDK, so a serialization regression fails the test suite rather than
production.

## venues.json — generated, not hand-written

The allowlist is produced by `services/market-data/cmd/gen-venues` from indexed
data plus on-chain verification, and committed:

```
go run ./services/market-data/cmd/gen-venues -out executor/venues.json   # from the repo root
go run ./services/market-data/cmd/gen-venues -dry-run                    # print, write nothing
```

It is a **build-time** tool. The executor never fetches its allowlist at
runtime: the security property is that the set of addresses it will call cannot
be influenced by the request path, or by a compromised indexer. A human reads
the diff; the file ships inside the image.

A venue is emitted only when all of these hold — encodable (its protocol maps to
a `VenueKind` implemented here), priceable (verified Chainlink feed for the
underlying on that chain), liquid (clears the TVL floor), verified on chain
(`symbol()`, `decimals()`, plus Aave's `getReservesList()` / Comet's
`baseToken()` / ERC-4626's `asset()` / Moonwell's `isMToken()` + `underlying()`),
and its id equals what market-data's `MakeID` produces. Everything else is
skipped with a printed reason, which is how you find out that an asset needs a
price feed or a new `VenueKind`.

The file carries a `_generated` block with the time and command; a bare JSON
array still loads, for a hand-written file behind `VENUES_PATH` in a test.

Aave's reserve id shape is read from the subgraph, never reconstructed: upstream
it is `underlying + addressesProvider` (the Pool then comes from that provider's
`getPool()`), while our own deployment keys reserves by the underlying alone and
carries the Pool as a field. Either way the Pool is proved by
`getReservesList()` before the venue is emitted.

### Moonwell (`ctoken`) — verified on Base, read-only, no transaction sent

| check | result |
|---|---|
| `isMToken()` on `0xEdc817A2…` (mUSDC) | `true`; `underlying()` → mainnet USDC, `symbol()` → `mUSDC` |
| `mint(uint256)` selector | `0xa0712d68`, pinned in `ctoken_selectors_are_canonical` |
| `redeemUnderlying(uint256)` selector | `0x852a12e3`, pinned in the same test |
| approval spender | the mToken itself — `mint` from an account with no allowance reverts inside USDC with `ERC20: transfer amount exceeds allowance`, i.e. it pulls with `transferFrom(msg.sender, mToken, amount)`. No comptroller approval exists. |
| recipient parameter | **none**, and no `mintTo`-style variant on the deployed contract. `mint`/`redeemUnderlying` credit `msg.sender`, which is correct only because the executor sends from the user's own wallet. Unlike ERC-4626/Aave/Comet, the destination is *not* assertable from the calldata. |

**Failures are return values, not reverts.** `redeemUnderlying(1e6)` from an
account with no position returns `9` (MATH_ERROR) in a transaction that
*succeeds*. Receipt status alone would book that as a confirmed withdrawal of
money that never moved — the worst bug available in this system. Some failures
do revert (`mint` on the paused mwrsETH market reverts with `mint is paused`; a
missing allowance reverts inside USDC), so the shape is inconsistent and cannot
be relied on either way.

So every `ctoken` call carries an expected success event and is only believed
when the receipt contains it:

| event | topic0 | confirmed against |
|---|---|---|
| `Mint(address,uint256,uint256)` | `0x4c209b5f…21c4f` | live logs on mUSDC |
| `Redeem(address,uint256,uint256)` | `0xe5b754fb…1a929` | live logs on mUSDC |

A mined `ctoken` call with no such event is reported `failed`, with a reason
saying the venue returned a failure code instead of reverting
(`a_mined_ctoken_call_without_its_event_is_a_failure`). This is also why a
ctoken call is confirmed even when it is the last in the sequence, where every
other kind is only submitted.

#### Comet call shapes, verified on the deployed contract

Comet needs its own `VenueKind` (`compound_v3`) because its supply is
`(asset, amount)`, not Aave's `(asset, amount, onBehalfOf, referralCode)` —
mapping one onto the other would put an amount where an address belongs.

Selectors were computed from the signatures with keccak and then confirmed
against the live proxy by `eth_call` (no transaction was sent). A garbage
selector reverts, so a call that executes is evidence the function exists:

| call | selector | probe result |
|---|---|---|
| `supply(address,uint256)` | `0xf2b9fdb8` | executes |
| `supplyTo(address,address,uint256)` | `0x4232cd63` | executes — **used** |
| `withdraw(address,uint256)` | `0xf3fef3a3` | executes |
| `withdrawTo(address,address,uint256)` | `0xc3b35a7e` | executes — **used** |
| `0xdeadbeef` (control) | — | `execution reverted` |

Three findings behind those choices:

- **`supply` credits `msg.sender`.** The executor sends from the user's own
  wallet, so that is already correct. The `To` variants are used anyway because
  they name the destination in the calldata, exactly as the ERC-4626 arm passes
  `receiver` and the Aave arm passes `onBehalfOf`. It is the same cost, it keeps
  all three adapters reading alike, and it means the "funds go to the user"
  property is assertable from the encoded bytes rather than from an assumption
  about `msg.sender`.
- **Comet is its own spender.** `supply(asset, 1)` from an account with no
  approval reverts with `ERC20: transfer amount exceeds allowance`, i.e. Comet
  pulls with `transferFrom(msg.sender, comet, amount)` directly. There is no
  separate router, so the existing `approve_call` applies unchanged with the
  Comet address as spender.
- **`type(uint256).max` means "the whole balance".** From a zero-balance
  account, `withdrawTo(to, asset, 1)` reverts (`0x14c5f7b6`) while
  `withdrawTo(to, asset, max)` succeeds — literal amounts are enforced, and max
  is special-cased. **Not implemented**: the wire contract asks for a USD
  amount, and "exit fully" is a different intent nothing expresses yet. It is
  the right primitive for a full exit when one is wanted, because Comet accrues
  interest continuously and an exact stored amount always leaves dust.

### Venue IDs

IDs follow market-data's `chain:project:poolID` (`venue.MakeID`).

- Both entries use the Aave subgraph's `Reserve.id` shape, lowercase
  `underlyingAsset` concatenated with lowercase `poolAddressesProvider`
  (`0xd449fed49d9c443688d6816fe6872f21402e41de`) — byte-identical to what
  market-data's `venue.MakeID` publishes:
  - `Base:aave-v3:0x036cbd53842c5426634e7929541ec2318f3dcf7e0xd449fed49d9c443688d6816fe6872f21402e41de`
  - `Base:aave-v3:0x42000000000000000000000000000000000000060xd449fed49d9c443688d6816fe6872f21402e41de`
- The Comet entry uses the Comet address in lowercase,
  `Base:compound-v3:0x571621ce60cebb0c1d442b5afb38b1663c6bf017`, matching the
  Compound III subgraph's `Market.id` that market-data feeds to `MakeID`.
- The WETH entry is allowlisted but not yet routable: `route.rs` refuses any
  non-USD-pegged symbol because sizing a deposit needs a price. That guard is
  unchanged.

Adding a venue means one JSON entry. Adding a *protocol* means one `VenueKind`
variant and one match arm in `src/venues.rs`.

## Layout

- `src/main.rs` — wiring: config, allowlist, router, graceful shutdown.
- `src/config.rs` — env config, refuses to start without Privy credentials.
- `src/venues.rs` — allowlist + ABI encoding. The security boundary.
- `src/route.rs` — request validation and submission sequencing.
- `src/privy.rs` — Privy wallet RPC client (signing only).
- `src/auth.rs` — Privy authorization signatures over the canonical payload.
- `src/rpc.rs` — read-only JSON-RPC receipt polling.

## swaps.json — the swap-path allowlist

A deposit into an asset that is not USDC buys it on Uniswap v3 first, then
supplies it if a venue holds that asset. The sequence is
`approve(router) → swap → approve(venue) → supply`, and the swap's `recipient` is
the user's own wallet — the output never lands in a contract this service
controls.

Paths are static and come only from this file. They were chosen by measuring
price impact on chain: the direct USDC pools for cbETH and wstETH are
effectively dead (-15.5% and -78.4% at $1,000), so those route through WETH,
while WETH and cbBTC are single-hop. Sizing is on the **input**: `amount_usd` is
USDC, and the quote decides how much of the output asset that buys.

| field | meaning |
|---|---|
| `from` / `to` | asset symbols; with `chain_id` they form the lookup key |
| `router` | SwapRouter02, also the approval spender |
| `quoter` | QuoterV2, called read-only over `eth_call` |
| `hops` | ordered; each is the token coming out of a pool plus its fee tier in hundredths of a bip. One hop uses `exactInputSingle`, more use `exactInput` with the packed path |

A withdrawal or rebalance of a non-USD asset is still refused: nothing sizes a
USD figure into an amount of WETH on the way *out*, and the swap leg only solves
the way in.
