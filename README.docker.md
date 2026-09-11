# Running the stack in Docker

One command, from a clean checkout:

```sh
cp .env.example .env      # then fill in the values listed below
docker compose up --build
```

Everything comes up in dependency order and waits on real health checks, not sleeps.

## What runs

| Service | Port (host) | What it does |
|---|---|---|
| `postgres` | 5432 | Wallet-service database. PG16, so `gen_random_uuid()` is built in — no pgcrypto needed. |
| `redis` | 6379 | Venue cache + `venues:updated` pubsub between market-data and the keeper. |
| `migrate` | — | One-shot. Applies `services/wallet/migrations/*.sql` in filename order, then exits 0. |
| `market-data-publisher` | — | Background poller. Fetches yield sources and republishes venues into Redis. No HTTP. |
| `market-data-api` | 8081 | Read-side API over the Redis venue set (`/venues`, `/venues/best`, `/assets`, `/sources`). |
| `wallet` | 8080 | Auth, baskets, portfolio, withdraw. The only client of the executor. |
| `executor` | 8082 | The only component that signs and submits transactions. Bounded by `venues.json`. |
| `keeper` | 8083 | Decides *when* to rebalance and asks the wallet service to do it. **Runs live, not dry-run.** |
| `client` | 3000 | Next.js frontend. |

Startup order: `postgres` + `redis` → `migrate` → market-data publisher/api → `wallet` → `executor` → `keeper` → `client`.

## Env vars you must set before the first run

In the repo-root `.env` (gitignored, read by compose via `env_file`; never baked into an image):

- `PRIVY_APP_ID`, `PRIVY_APP_SECRET` — **required**. The wallet service fetches its
  verification key from Privy's API at boot and refuses to start without them; the
  executor refuses to start without them. Both are fail-closed by design.
- `PRIVY_AUTHORIZATION_PRIVATE_KEY` — needed for wallets owned by an authorization key
  or key quorum. The executor warns and sends unsigned requests without it.
- `GRAPH_API_KEY` — the publisher exits if the Graph source cannot be built.
- `TOKEN_API_JWT` — Graph Token API, used for portfolio balances.
- `KEEPER_SECRET` — required to run the keeper **live**. Unset, the keeper forces
  dry-run and submits nothing.
- `BASE_RPC_URL` — gas and ETH price. The keeper abandons a whole pass if it cannot
  reach this; the executor uses it to wait for receipts.

Also add the client's public vars to the root `.env` (compose passes them as build args):
`NEXT_PUBLIC_PRIVY_APP_ID`, `NEXT_PUBLIC_PRIVY_SIGNER_ID`, `NEXT_PUBLIC_PRIVY_POLICY_ID`.
See `client/.env.example` for what each one is.

**Containers need outbound DNS and HTTPS.** Privy, The Graph and the RPC node are all
external. If your network blocks egress, `wallet`, `executor` and `keeper` will not start —
that is the intended behaviour, not a bug to work around.

### `NEXT_PUBLIC_*` is a build-time thing

Next inlines `NEXT_PUBLIC_*` into the JS bundle when it builds. Setting them in
`environment:` does nothing. They are compose `build.args` instead, so changing one means:

```sh
docker compose build client && docker compose up -d client
```

`NEXT_PUBLIC_WALLET_URL` and `NEXT_PUBLIC_MARKET_DATA_URL` stay `http://localhost:8080` /
`:8081` — the **browser** resolves them, and it is not on the compose network.

### Container hostnames vs localhost

Compose overrides these per service so your `.env` can keep localhost values for running
things directly on the host. Both modes work off the same file.

```
DATABASE_URL     postgres://sweem:sweem@postgres:5432/sweem?sslmode=disable
REDIS_URL        redis://redis:6379
MARKET_DATA_URL  http://market-data-api:8081
EXECUTOR_URL     http://executor:8082
WALLET_URL       http://wallet:8080
```

### Pointing the stack at a different network

These change **together** — they are grouped under the `x-chain-env` anchor at the top of
`docker-compose.yml`:

- `BASE_RPC_URL` — shared by executor, wallet and keeper. Defined once, referenced four times.
- `CHAINS`, `DEFAULT_CHAIN` — which chain the publisher polls and the wallet defaults to.
- `MORPHO_SUBGRAPH_ID` (and any other subgraph ID vars) — deployments are per-chain.
- `CHAINLINK_ETH_USD` — the price feed address, per-chain.
- `executor/venues.json` — the security boundary. Its entries carry `chain_id` and target
  addresses, so it must be swapped **and the executor image rebuilt** (`VENUES_PATH` points
  at the copy baked into the image).

## Common operations

```sh
docker compose logs -f wallet          # follow one service
docker compose ps                      # health status of everything
curl localhost:8080/health             # ports are bound to the host

docker compose down -v                 # reset: drops the pgdata volume
docker compose up --build              # migrations re-run from scratch

docker compose run --rm migrate        # re-apply migrations only
```

## Images

Go services share one multi-stage `Dockerfile.golang`, parameterized by
`--build-arg CMD_PATH=<path to the cmd package>`. `go mod download` is its own layer, so a
source edit does not re-fetch dependencies. `CGO_ENABLED=0`, final stage alpine (not
distroless — the compose health checks need busybox `wget` in the image).

The executor builds its dependencies against a stub `main.rs` in a separate layer, so
editing `src/` does not recompile every crate.

The client image is ~2 GB because it ships the full `node_modules` tree. Adding
`output: "standalone"` to `client/next.config.ts` cuts it to a couple of hundred MB —
that file belongs to the client work, so it was left alone here; make the change there and
the runner stage can drop to `COPY .next/standalone`.

The client installs with bun 1.3.11 and builds/runs on node: `bun run build` segfaults
inside bun 1.3.11 on linux/arm64 (a Bun crash, not a Next one).
