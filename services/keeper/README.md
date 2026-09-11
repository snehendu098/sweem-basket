# keeper

The keeper is what makes the protocol autonomous rather than manual. It runs
continuously, watches every subscriber's positions for yield drift, and triggers
a rebalance when moving the money is worth more than it costs.

Today a user must click "rebalance". The keeper removes the click.

## Boundaries

The keeper decides **when**. The wallet service still does **how**.

```
keeper --(subscribe)--> Redis venues:updated
keeper --(read)-------> Postgres     (positions, subscriptions, users, executions)
keeper --(read)-------> market-data  GET /venues
keeper --(read)-------> Base RPC     gas price, ETH/USD, transaction receipts
keeper --(POST)------->  wallet svc  POST /v1/baskets/{id}/rebalance
```

The keeper never calls the executor and never signs anything. Signing authority
stays in exactly one place, which means the keeper can be restarted, duplicated
or crashed without putting funds at risk.

Postgres is read-only for the keeper with one exception: the pending-execution
sweeper below, which resolves rows the wallet service could not. Nothing else
in the system will.

## Triggers

Two ways to wake up, one evaluation pass:

1. Redis pubsub on `venues:updated` — new rate data is the natural moment to
   re-evaluate.
2. A periodic tick (`KEEPER_INTERVAL`) as the safety net for when the
   subscription drops.

Both feed one buffered trigger channel (capacity 1), so notifications arriving
during a pass coalesce into exactly one follow-up run. A pass that starts within
`KEEPER_DEBOUNCE` of the previous one is dropped.

## A pass, in order

1. **Sweep pending executions** — resolve what already happened.
2. **Price a rebalance** from live chain data.
3. **Evaluate drift** for every eligible subscription.

Step 1 comes first because reasoning about positions we already know are stale
is how the keeper would act on money that is not where it thinks it is. Step 2
comes before step 3 because a pass that cannot price a move makes no decisions
at all.

## Sweeping pending executions

The executor polls for a receipt and can return `pending` when that poll times
out — the transaction is probably in the mempool and will land. The wallet
service records the row `pending` with the tx hash and deliberately does not
write the position, because it does not yet know whether the money moved.
Nothing else resolves those rows; the keeper does.

For every `executions` row with `status = 'pending'`, a `tx_hash`, and an age
over `PENDING_SWEEP_MIN_AGE`, the keeper fetches the receipt from `BASE_RPC_URL`:

| Receipt | Action |
|---|---|
| `status = 0x1` | Mark `confirmed` and write the position the wallet service withheld |
| `status = 0x0` | Mark `failed`. No position. |
| none yet | Leave `pending`, try again next pass |
| none, and older than `PENDING_SWEEP_GIVE_UP` | Mark `failed` with an error naming the tx hash, and log it at error level — a transaction unseen for a day wants a human |

The chain and project the position needs are not columns on `executions`; they
come from parsing the venue ID (`chain:project:pool`). The venue's current APY
comes from market-data, and when market-data does not carry that venue the row's
existing `entry_apy` is kept rather than an invented one.

Two things keep this safe to run repeatedly and from more than one instance:
the row is claimed with a conditional `UPDATE … WHERE status = 'pending'`
(only one caller can win), and the position write is an upsert keyed exactly as
the wallet service keys it (`user_id, basket_id, asset`), so it cannot
double-count. In `--dry-run` the sweeper logs what it would resolve and changes
nothing.

## The decision, in prose

For every `active` subscription whose user has `delegated = true` and a non-empty
`privy_wallet_id`:

1. Load that user's positions for the basket.
2. For each position, ask market-data for the venues carrying that asset on that
   chain above `MIN_VENUE_TVL_USD`.
3. Score every venue on **reward-discounted** APY:
   `effectiveAPY = apy_base + apy_reward * REWARD_DISCOUNT`. The best venue is
   the highest-scoring one — which is not necessarily market-data's `/venues/best`,
   because that ranks on the raw rate. The position's current venue is scored the
   same way, falling back to the stored `entry_apy` if it has dropped out of
   market-data (stale and usually high, so the fallback makes the keeper *less*
   eager to move, not more).
4. `drift = bestEffectiveAPY - currentEffectiveAPY`, in percentage points.
5. Run the guards, then the breakeven.
6. If any leg of a basket qualifies, POST once to the wallet service's rebalance
   endpoint for that basket, acting as that user. The wallet service re-checks
   delegation, drift and everything else — the keeper's decision is a trigger,
   not an authorization.

### Breakeven

```
gainPerYear     = positionUSD * (bestAPY - currentAPY) / 100     # USD/year
horizonYears    = BREAKEVEN_HORIZON / (365*24h)                  # unitless
gainUSD         = gainPerYear * horizonYears                     # USD
costUSD         = gasCostUSD * SAFETY_MARGIN                     # USD, one leg
move            = gainUSD > costUSD
```

`BREAKEVEN_HORIZON` defaults to `MIN_HOLD_PERIOD`: only pay the gas if the extra
yield is recovered before we would even be allowed to move again. That is the
strictest possible reading. Set it longer (e.g. `720h`) if you believe positions
actually sit longer than the hysteresis window.

All APYs are percentage points (`5.0` means 5%/year); all amounts are USD.

### Where `gasCostUSD` comes from

Nothing here is a configured guess. Every number is read live:

```
gasCostUSD = gasUnits * gasPriceWei * ethPriceUSD / 1e18
```

- **`gasPriceWei`** — `eth_feeHistory` over the last 5 blocks on `BASE_RPC_URL`:
  the next block's base fee plus the median 50th-percentile priority tip. A
  percentile beats `eth_gasPrice` here because one outlier block should not
  reprice every user's decision. Fetched once per pass.
- **`ethPriceUSD`** — the Chainlink ETH/USD aggregator on Base at
  `0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70`, read with `eth_call` on
  `latestRoundData()`. Onchain, no API key, no rate limit. `decimals()` is read
  rather than assumed, and an answer whose `updatedAt` is older than
  `PRICE_MAX_AGE` is rejected outright. Cached for `PRICE_CACHE_TTL`.
- **`gasUnits`** — measured, not guessed. Successful Base mainnet transactions
  sampled around block 51,089,191 (2026-09-09):

  | Operation | Observed `gasUsed` | Constant | Sample tx |
  |---|---|---|---|
  | `Pool.withdraw()` | 170,504 – 348,276 | 350,000 | `0x76da5f011b07dc4303e349573c028fa3cc0793190878d32a4695b5143804df05` |
  | `USDC.approve()` | 55,377 – 55,761 | 56,000 | `0xd6f74b207344255558d44ee11d022052ba9adfc32b36f96103c866e96672064a` |
  | `Pool.supply()` | 142,496 – 148,767 | 150,000 | `0xd55b52076ec75e44e872cb0bc7717c097b0c6ba9c9186939f06404283c60764f` |
  | **rebalance total** | | **556,000** | |

  **This is a known approximation.** The keeper does not build the calldata — the
  executor does — so it cannot `eth_estimateGas` the exact transaction. Withdraw
  in particular spreads widely with accrued interest and reward claims, so the
  constant sits at the top of the observed range: over-pricing makes the keeper
  too cautious, under-pricing makes it churn. The full transaction hashes are in
  the comment on `internal/gas`.

  Also approximate in the same direction: this prices L2 execution only. Base's
  L1 data-availability fee is not included, and is not knowable before the
  calldata exists. `SAFETY_MARGIN` absorbs it.

At the readings above (0.00945 gwei, $2478/ETH) a rebalance costs about **$0.013**
— which is why the strict 6h horizon is workable: a 2pp improvement clears
breakeven on a position of roughly $1,400.

**If any input is missing or stale, the pass is skipped.** It is not downgraded
to a default. The skip is logged and counted in `/health` under
`gas_cost_unavailable`. A rebalance decided on fabricated cost data is worse
than no rebalance.

`GAS_COST_USD` overrides the whole calculation with a fixed figure. It exists
for tests and dry runs, it logs a warning when set, and it is explicitly **not**
a fallback when the live fetch fails.

### Guards

Applied cheapest-first, before the breakeven. Every outcome, including every
skip, is logged with its reason and counted in `/health`.

| Guard | Reason code | Default | Why |
|---|---|---|---|
| Already in the best venue | `already_in_best_venue` | — | Nothing to do |
| Position floor | `position_below_floor` | `MIN_POSITION_USD=5` | Gas dominates small positions |
| Rate limit | `rate_limited` | `MAX_REBALANCES_PER_DAY=4` | Caps damage from bad data or a bug |
| Hysteresis | `within_hold_period` | `MIN_HOLD_PERIOD=6h` | Two venues within noise of each other would otherwise ping-pong funds until gas eats the position |
| Absolute drift floor | `drift_below_floor` | `MIN_DRIFT_APY=0.5` | Never move on noise, whatever the breakeven says |
| Breakeven | `gain_below_breakeven` | see above | The actual economics |

Hysteresis and the rate limit both read the `executions` table (`kind='rebalance'`,
`status <> 'failed'` — a failed leg burned gas but moved no money, so it must not
lock a user out of a retry). Within one pass the budget is shared across a user's
baskets, so a user subscribed to several cannot spend the cap twice.

**The rate limit counts legs, not passes.** One rebalance of a three-asset basket
writes three `executions` rows, so the default of 4 allows roughly one full
rebalance per day for a 3-asset basket. Raise it if baskets are wide.

### Known limitation: undisclosed rewards

The reward discount can only discount what the source separates. Moonwell USDC
currently reports ~14.5% because the Messari subgraph folds WELL emissions into a
single LENDER rate: `apy_reward` is 0 and `apy_base` is inflated. That venue
therefore dodges the discount entirely and looks better than it is. Fixing it
means splitting the rate at the market-data source, not here.

## Environment

| Var | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | — | Postgres DSN (wallet service's DB, read-only use) |
| `REDIS_URL` | `redis://localhost:6379` | For the `venues:updated` subscription |
| `MARKET_DATA_URL` | `http://localhost:8081` | market-data service |
| `WALLET_URL` | `http://localhost:8080` | wallet service |
| `KEEPER_ADDR` | `:8083` | Health server address |
| `KEEPER_SECRET` | — | Shared secret sent as `X-Keeper-Secret`. **Unset ⇒ dry run.** |
| `KEEPER_INTERVAL` | `5m` | Safety-net tick |
| `KEEPER_DEBOUNCE` | `30s` | Minimum gap between passes |
| `KEEPER_PASS_TIMEOUT` | `5m` | Hard deadline for one pass |
| `DRY_RUN` | `false` | `true` forces dry run |
| `MIN_DRIFT_APY` | `0.5` | Absolute drift floor, percentage points |
| `MIN_POSITION_USD` | `5` | Position floor (testnet-scaled) |
| `MIN_HOLD_PERIOD` | `6h` | Hysteresis window per user+asset |
| `BREAKEVEN_HORIZON` | `MIN_HOLD_PERIOD` | Assumed holding period when pricing a move |
| `REWARD_DISCOUNT` | `0.5` | Weight on `apy_reward` |
| `GAS_COST_USD` | unset | Fixed USD cost override. Tests and dry runs only, never a fallback |
| `BASE_RPC_URL` | `https://sepolia.base.org` | Base node for gas price, ETH/USD and receipts (same var the executor uses). The public endpoint rate-limits and caps `eth_getLogs` at 10k blocks. |
| `CHAIN_ID` | `84532` | selects the Chainlink ETH/USD aggregator (`8453` = Base mainnet) |
| `CHAINLINK_ETH_USD` | `0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70` | ETH/USD aggregator on Base |
| `GAS_UNITS_REBALANCE` | `556000` | Measured withdraw + approve + deposit |
| `PRICE_MAX_AGE` | `1h` | Reject an oracle answer older than this |
| `PRICE_CACHE_TTL` | `60s` | ETH price cache |
| `PENDING_SWEEP_MIN_AGE` | `2m` | Ignore pending rows younger than this |
| `PENDING_SWEEP_GIVE_UP` | `24h` | Unobserved transaction is called failed after this |
| `SAFETY_MARGIN` | `1.5` | Gain must beat cost by this multiple |
| `MAX_REBALANCES_PER_DAY` | `4` | Rebalance legs per user per 24h |
| `MIN_VENUE_TVL_USD` | `50000` | Liquidity floor for candidate venues (testnet-scaled) |
| `LOG_LEVEL` | `0` (info) | slog level |
| `ENV_FILE` | `.env` | Dotenv path |

## Auth to the wallet service

The keeper cannot mint a Privy access token for a user, so it authenticates as
itself and names the user it acts for:

```
POST /v1/baskets/{id}/rebalance
X-Keeper-Secret: <KEEPER_SECRET>
X-Acting-User:   did:privy:...
```

The wallet service must accept this pair as an alternative to the Bearer token
(constant-time secret comparison, and only for the rebalance route). Until it
does, the keeper runs in dry run.

## Running

Dry run — evaluates and logs every decision against live data, calls nothing.
This is the safe way to demo the decision logic:

```sh
DATABASE_URL=postgres://... go run ./services/keeper/cmd/keeper --dry-run
```

Dry run is also the default whenever `KEEPER_SECRET` is unset; the keeper logs a
warning rather than pretending it can act. Live:

```sh
DATABASE_URL=postgres://... KEEPER_SECRET=... go run ./services/keeper/cmd/keeper
```

Health:

```sh
curl -s localhost:8083/health | jq
```

returns the last pass time and duration, the live gas cost the pass was priced
on, subscriptions evaluated, legs evaluated, legs moved, legs skipped broken out
by reason (including `gas_cost_unavailable`), the sweeper's counts (checked,
confirmed, failed, still pending, given up), and the error count.

## Tests

`go test ./services/keeper/...` — table-driven, stdlib only, no network and no
DB. `internal/policy` is pure and covers the breakeven, hysteresis, the reward
discount and the rate limiter. `internal/gas` covers the cost arithmetic against
hand-computed examples, the staleness rejection and the Chainlink decoding, with
the price and gas-price sources injected. `internal/engine` drives a whole pass
and the sweeper — including the give-up path and the double-run idempotency
case — against in-memory doubles.
