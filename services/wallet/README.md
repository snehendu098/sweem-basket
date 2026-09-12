# wallet service

Primary backend. Owns auth, baskets, portfolio, and is the **only** client of the
Rust executor.

## Model

Copy-trading, not a pooled vault. Funds stay in each user's own Privy embedded
wallet. A basket is a published weight config; a subscriber's own wallet follows
those weights via a Privy delegated signer. No share accounting, no NAV relayer,
no custody.

## Talks to

| Dependency | Why |
|---|---|
| Postgres | users, baskets, weights, subscriptions, positions, executions |
| market-data svc | `GET /venues/best` — where each asset should be routed |
| executor (Rust) | signs and submits transactions. Nothing else may call it. |
| Token API (The Graph) | onchain ERC-20 balances — the truth the DB is checked against |
| Chainlink on Base | USD prices, read over `eth_call`. No number is ever assumed. |
| Privy | access-token verification (ES256) |

## Run

```bash
psql "$DATABASE_URL" -f services/wallet/migrations/0001_init.sql
psql "$DATABASE_URL" -f services/wallet/migrations/0002_privy_wallet_id.sql
psql "$DATABASE_URL" -f services/wallet/migrations/0003_execution_steps.sql
go run ./services/wallet/cmd/api
```

Env (see `.env.example` at repo root):

| Var | Default | Notes |
|---|---|---|
| `DATABASE_URL` | — | required |
| `PRIVY_APP_ID` | — | required; checked as the token audience |
| `PRIVY_APP_SECRET` | — | required, unless `PRIVY_VERIFICATION_KEY` is set |
| `PRIVY_VERIFICATION_KEY` | — | optional override; skips the boot-time fetch |
| `MARKET_DATA_URL` | `http://localhost:8081` | |
| `EXECUTOR_URL` | `http://localhost:8082` | |
| `MIN_VENUE_TVL_USD` | `50000` | liquidity floor for routing (testnet-scaled) |
| `DEFAULT_CHAIN` | `Base` | also the network queried for onchain balances |
| `REBALANCE_THRESHOLD_APY` | `0.5` | drift, in percentage points, needed to move a position |
| `MAX_SLIPPAGE_BPS` | `50` | passed through to the executor on every route |
| `KEEPER_SECRET` | — | shared secret for the keeper; **empty disables the path** |
| `TOKEN_API_JWT` | — | optional; unset ⇒ portfolio returns `onchain_available: false` |
| `TOKEN_API_URL` | `https://token-api.thegraph.com` | |
| `BASE_RPC_URL_8453` / `BASE_RPC_URL_84532` | public Base endpoints | one Chainlink client per chain; a basket's own `chain` picks which one prices its assets |
| `DEFAULT_CHAIN` | `base` | canonical label a basket gets when the request does not name one (`base`, `base-sepolia`); an unknown value is fatal at boot |
| `PRICE_MAX_AGE` | `1h` | grace *on top of* each feed's own measured heartbeat |
| `PRICE_CACHE_TTL` | `1m` | per-feed, so one request is not a dozen RPC calls |
| `WALLET_ADDR` | `:8080` | |

### Verification key

Fetched once at boot from `GET https://api.privy.io/v1/apps/{app_id}`
(HTTP Basic `app_id:app_secret` plus a `privy-app-id` header) and cached for the
process lifetime — one less thing to configure, and it cannot go stale. Privy
has no JWKS endpoint.

The API returns `verification_key` as an SPKI PEM **on a single line**, header
and footer glued to the body, which `encoding/pem` will not parse; the verifier
rewraps it at 64 characters. A key pasted from the dashboard already has its
newlines and passes through untouched.

Set `PRIVY_VERIFICATION_KEY` to skip the fetch entirely — useful offline. If the
fetch fails and no override is set, **the service does not start**. It will not
run unable to verify tokens. Everything else is unchanged: ES256 pinned via
`WithValidMethods`, issuer `privy.io`, audience = app ID, expiry required.

The app secret is sent in the Basic auth header only. It never reaches a log
line or an error message — failures report the HTTP status and nothing more.

## API

All `/v1/*` routes require `Authorization: Bearer <privy access token>`.

| Method | Path | Purpose |
|---|---|---|
| GET | `/health` | db + executor reachability |
| GET | `/public/baskets?limit=` | **no auth** — public baskets, narrowed view |
| GET | `/public/baskets/{id}` | **no auth** — one public basket; 404 if private |
| POST | `/v1/me` | bind Privy DID → wallet address + `privy_wallet_id`; record delegation |
| GET | `/v1/me` | current user |
| POST | `/v1/baskets` | create; weights must sum to 10000 bps |
| GET | `/v1/baskets?scope=mine\|public` | list, with weights and `subscribed` |
| GET | `/v1/baskets/{id}` | basket + weights + `subscribed` |
| POST | `/v1/baskets/{id}/subscribe` | same preconditions as deposit |
| DELETE | `/v1/baskets/{id}/subscribe` | exit |
| GET | `/v1/baskets/{id}/plan?amount_usd=` | **read-only** routing preview |
| POST | `/v1/baskets/{id}/deposit` | execute the plan; per-leg results |
| POST | `/v1/baskets/{id}/withdraw` | take funds back out; `amount_usd` or `all` |
| POST | `/v1/baskets/{id}/rebalance` | move drifted positions; also the keeper's one route |
| GET | `/v1/portfolio` | positions, live APY, drift vs best venue |
| GET | `/v1/executions` | audit trail |

### `/public/*` — discovery without a wallet

`is_public` means discoverable. Requiring a Privy token to browse a public
listing would put the whole social layer behind a login, so these two routes
sit outside the `/v1/` authed mux and take no `Authorization` header. CORS
already applies at the server level.

They are not the authed handlers with the check removed — the response is
narrowed on purpose, since this is an anonymous surface on a service that
otherwise moves money:

```json
{
  "id": "...", "name": "...", "description": "...", "chain": "Base",
  "fee_bps": 25,
  "weights": [{"asset": "USDC", "weight_bps": 10000}],
  "created_at": "..."
}
```

The list route returns an array of exactly that object (`[]` when empty).

- **Public only.** A private basket answers `404`, not `403`: an anonymous
  caller must not be able to confirm that an id exists.
- **No `creator_id`.** It is an internal UUID. Creator identity is omitted
  entirely rather than leaked; add a stable public handle when the UI needs one.
- **No `subscribed`.** Per-caller, and there is no caller. Omitted, not `false`.
- **Nothing from `positions` or `executions`** — no counts, balances, or
  positions.
- **`limit` is clamped to 100** (default 50), so one anonymous request cannot
  ask for the whole table.
- Weights come batched from the same query as the list; no N+1 on an
  unauthenticated route.

`GET /v1/baskets?scope=public` is unchanged and stays the route for logged-in
users, who also get `subscribed`.

### Prices — Chainlink on Base, or nothing

`internal/shared/prices` reads `latestRoundData()` over `eth_call` on `BASE_RPC_URL`.
`decimals()` is read from each feed rather than assumed. Symbol → aggregator
lives in one table (`prices.Feeds`); every address there was verified by calling
`description()` on Base mainnet, and unknown symbols return unavailable rather
than a default. Extend the table the same way. The package is shared with
market-data, which prices venue TVL from the same aggregators.

Assets with no direct USD aggregator on Base (wstETH, weETH, rETH, ezETH) are
composed from `prices.RatioFeeds`: `ratio x ETH/USD`, with both legs
staleness-checked against their own heartbeat.

Staleness is judged per feed, not globally. Each entry carries a heartbeat
**measured onchain** by walking `getRoundData` over recent rounds: stablecoin
feeds publish every 24h, ETH and BTC feeds every few minutes. A round is stale
past `heartbeat + PRICE_MAX_AGE`. A single one-hour bound would have marked the
perfectly healthy USDC feed stale and made every USDC basket unroutable — that
was caught by reading the live chain, not the fixtures.

An unpriceable asset is never routed: `/plan` shows the leg with
`price_usd: null` and no venue, and `/deposit` skips it as `skipped` with the
reason. Money does not move on a number we invented.

```json
{
  "basket_id": "…", "chain": "Base", "amount_usd": 1000, "blended_apy": 4.61,
  "legs": [
    { "asset": "USDC", "weight_bps": 6000, "amount_usd": 600,
      "price_usd": 0.99990801, "amount_token": 600.055,
      "venue": { "id": "Base:aave-v3:…", "project": "aave-v3", "apy": 3.74 },
      "reason": "highest net APY above the TVL floor" },
    { "asset": "PEPE", "weight_bps": 4000, "amount_usd": 400,
      "price_usd": null, "amount_token": null,
      "reason": "no Chainlink price feed for PEPE on Base; value unknown" }
  ]
}
```

### `/portfolio` — drift is the rebalance signal, onchain is the truth

Each position carries `current_apy` and `drift_apy` (best available minus
entry). Positive drift means the money should move.

`positions` is our own bookkeeping. `onchain` is what The Graph's Token API
reports the wallet actually holds on Base, and `reconciled` per position says
whether the two agree within tolerance (2%, floor $1).

Values come from Chainlink feeds on Base, read live. Nothing is assumed:

- No feed for the asset, a round older than `PRICE_MAX_AGE`, or an unreachable
  RPC ⇒ `onchain_usd: null` with a `value_reason`. Never a fabricated $1.
- Only an exact symbol match is reconciled. A position held as a venue receipt
  token (`mUSDC`, `aBasUSDC`) has a redemption rate we do not read onchain, so
  it reports `reconciled: false` with a reason rather than pretending 1:1.

Without `TOKEN_API_JWT` the endpoint still answers, with
`"onchain_available": false` and no `onchain` block. It never fails on the
Token API.

```json
{
  "wallet_address": "0x…", "total_usd": 1000, "blended_apy": 14.5,
  "positions": [
    { "asset": "USDC", "venue_id": "Base:moonwell:0xedc8…", "amount_usd": 600,
      "entry_apy": 14.52, "current_apy": 14.52, "drift_apy": 0,
      "onchain_usd": 601.2, "reconciled": true },
    { "asset": "PEPE", "amount_usd": 100, "onchain_usd": null,
      "reconciled": false,
      "value_reason": "no Chainlink price feed for PEPE on Base; value unknown" }
  ],
  "onchain_available": true,
  "onchain": { "chain": "base", "token_count": 3, "balances": [
    { "contract": "0x833589…", "symbol": "USDC", "decimals": 6,
      "amount": "601200000", "value": 601.2, "network": "base" }
  ] }
}
```

### `/deposit` and `/rebalance` — partial failure is normal

Both require `delegated: true` **and** a non-empty `privy_wallet_id`; without
either the executor cannot act, so they return `412` naming what is missing.

Legs are independent. Each leg writes its `executions` row *before* the executor
call — a crash mid-call leaves a visible `pending` row, never an invisible
transaction. One failed leg never rolls back or hides a succeeded one.

Three outcomes per leg, not two:

| Leg status | Row | Position | Meaning |
|---|---|---|---|
| `submitted` | `submitted`/`confirmed` | written | the money moved |
| `pending` | stays `pending`, tx hash kept | **not** written | the executor's receipt poll timed out. Not a failure — the tx is probably still in the mempool, and calling it failed would invite a duplicate submission of money that already moved |
| `failed` | `failed` + error | not written, or marked idle | a step reverted; the executor stopped and did not submit the rest of the sequence |

The response is `200` only when every leg settled successfully, and
`207 Multi-Status` when any leg is `failed` **or** `pending` — an unsettled leg
is not a success. Never a bare `500` that hides which legs moved money.

Deposit amounts are split in whole cents by the largest-remainder method, so the
legs always sum back to exactly what the user sent.

### Idle funds after a partial sequence

A rebalance is withdraw → approve → deposit. If step 0 confirms and a later step
reverts, the money is out of the old venue and sitting in the user's wallet —
not lost, but not earning. That leg's position is rewritten to
`venue_id: "idle:wallet"`, `project: "wallet"`, `entry_apy: 0` rather than left
claiming a venue that no longer holds it. No schema change: the sentinel makes
the next `/rebalance` see full drift and re-place the funds, and because there
is nothing to withdraw it routes them as `action: "deposit"`.

Every leg carries the executor's `steps` trace, persisted to `executions.steps`
(JSONB) and returned by `GET /v1/executions`. The field is absent when the
request was rejected before anything was submitted.

```bash
curl -XPOST -H "Authorization: Bearer $TOKEN" \
  -d '{"amount_usd": 1000}' localhost:8080/v1/baskets/$ID/deposit
```

```json
{
  "basket_id": "…", "amount_usd": 1000, "submitted_usd": 600,
  "failed_legs": 1, "pending_legs": 1,
  "legs": [
    { "asset": "USDC", "amount_usd": 500, "venue_id": "Base:moonwell:0xedc8…",
      "project": "moonwell", "apy": 14.52, "execution_id": "…",
      "tx_hash": "0x…", "status": "submitted",
      "steps": [{ "step": 0, "tx_hash": "0xaaa…", "outcome": "confirmed" },
                { "step": 1, "tx_hash": "0xbbb…", "outcome": "confirmed" }] },
    { "asset": "WETH", "amount_usd": 400, "venue_id": "Base:aave-v3:0x…",
      "execution_id": "…", "status": "failed",
      "reason": "executor: status 502: upstream rpc timeout" },
    { "asset": "cbBTC", "amount_usd": 100, "venue_id": "Base:moonwell:0x…",
      "execution_id": "…", "tx_hash": "0xccc…", "status": "pending",
      "reason": "submitted but not yet confirmed; receipt poll timed out" }
  ]
}
```

`/rebalance` takes no body. It walks the basket's positions, skips any whose
drift is at or below `REBALANCE_THRESHOLD_APY`, and routes the rest with
`from_venue_id` = current, `to_venue_id` = best.

### `/withdraw` — the way out

Exiting a subscription only stops future moves; it does not return funds. This
is what actually gets the money back, and without it a user could deposit but
never leave.

```bash
curl -XPOST -H "Authorization: Bearer $TOKEN" \
  -d '{"amount_usd": 250}' localhost:8080/v1/baskets/$ID/withdraw
# or take everything:
curl -XPOST -H "Authorization: Bearer $TOKEN" \
  -d '{"all": true}' localhost:8080/v1/baskets/$ID/withdraw
```

A partial amount is split across the basket's positions in proportion to what
each currently holds, so the allocation keeps its shape on the way out. The
split uses the same whole-cent largest-remainder allocator as `/deposit`, so the
legs sum to exactly the requested amount.

Per-leg structure, `pending` handling and 200/207 semantics are identical to
`/deposit`. On a confirmed leg the position is reduced, or deleted once it
reaches zero. On `pending` it is left alone — same reasoning as deposit, we do
not yet know whether the funds moved. A position already at `idle:wallet` has no
venue to withdraw from, so it is skipped with a reason and excluded from the
split, which means a requested amount is drawn only from positions that can
supply it. Asking for more than the basket holds is a 400 naming the amount
available, rather than a quiet partial withdrawal.

```json
{
  "basket_id": "…", "amount_usd": 250, "withdrawn_usd": 250,
  "failed_legs": 0, "pending_legs": 0,
  "legs": [
    { "asset": "USDC", "amount_usd": 150, "from_venue_id": "Base:moonwell:0x…",
      "execution_id": "…", "tx_hash": "0x…", "status": "submitted" },
    { "asset": "WETH", "amount_usd": 100, "from_venue_id": "Base:aave-v3:0x…",
      "execution_id": "…", "tx_hash": "0x…", "status": "submitted" }
  ]
}
```

### Keeper access — one route, by construction

The keeper cannot mint Privy tokens, so it authenticates with a shared secret
and names the user it acts for:

```
POST /v1/baskets/{id}/rebalance
X-Keeper-Secret: $KEEPER_SECRET
X-Acting-User: did:privy:…
```

This is the **only** route that accepts it, and the scoping is structural: the
route is registered on its own pattern with the keeper-aware middleware, while
everything else is mounted behind the token-only one. The keeper path cannot be
reached elsewhere even if a future route is added carelessly. A secret that
reached `/deposit` would let whoever holds it move funds into arbitrary venues.

The keeper's decision is a **trigger, not an authorization**: `canExecute` still
applies, so it cannot act for a user who never delegated or has no
`privy_wallet_id`. An empty `KEEPER_SECRET` disables the path outright — it is
never a bypass. Requests arriving this way log `actor=keeper`.

The secret is compared as a SHA-256 digest of both sides:
`subtle.ConstantTimeCompare` short-circuits on a length mismatch, so comparing
the raw strings would leak the real secret's length through timing.

## Not implemented yet

- Nothing sweeps `pending` executions after the fact. A leg whose receipt poll
  timed out keeps its tx hash and stays `pending` until someone looks.

## Routing is allowlist-aware

market-data indexes every venue it can see; the executor can only encode calldata
for the ones in its allowlist. Proposing an indexed-but-unexecutable venue is a
route that fails after an execution row already exists, so routing filters the
rate table against `GET /venues` on the executor (fetched once, cached for five
minutes, never defaulted to "everything is allowed" — a failed fetch fails the
leg).

When the best indexed venue is not executable, the plan routes to the best one
that is and says so in the leg's `reason`: *"best rate is 14.50% at moonwell,
which this executor cannot transact; routing to aave-v3 at 4.20% instead"*.
Discovery surfaces (`/venues`, `/assets` on market-data) keep showing the real
best rate — a rate we cannot reach is worth naming, not hiding.
