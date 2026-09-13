# infra

One EC2 instance running the same `docker-compose.yml` as local, behind Caddy
for TLS. Terraform makes the box, Ansible puts the stack on it.

```
basket.sweem.org         Vercel          the UI
api.basket.sweem.org     EC2 :443        wallet API
market.basket.sweem.org  EC2 :443        market-data API
```

The executor and the keeper are deliberately absent from that list. The executor
holds the Privy authorization key and the keeper can move user funds; both stay
on the docker network with no host port and no proxy entry. Postgres and Redis
are closed the same way.

## Credentials

AWS keys live in the repo-root `.env` alongside everything else:

```
AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...
AWS_REGION=us-east-1
```

`make` sources that file before calling Terraform, which reads `AWS_*` from the
environment. Running `terraform` directly does not pick them up — use the make
targets, or source `.env` yourself first.

**Those keys never reach the instance.** The playbook filters every `AWS_*`,
`TF_VAR_*` and `TF_TOKEN_*` line out of the `.env` it writes to the box: the
server has no business holding keys to the account that created it. Everything
else is copied verbatim at `0600`.

## Deploy

```bash
cd infra
cp terraform/terraform.tfvars.example terraform/terraform.tfvars   # set acme_email
make init
make apply
cd terraform && terraform output dns_records
```

**Create the two A records it prints, and wait for them to resolve.** Caddy
gets certificates over HTTP-01, so the hostnames must point at the Elastic IP
before the playbook runs or the TLS step fails.

```bash
cd infra && make deploy
```

Nothing is baked into an image and nothing is committed.

## DNS

| type  | name                      | value                          |
|-------|---------------------------|--------------------------------|
| A     | `api.basket.sweem.org`    | the Elastic IP                 |
| A     | `market.basket.sweem.org` | the Elastic IP                 |
| CNAME | `basket.sweem.org`        | whatever Vercel gives you      |

## Vercel

Set these on the project, then redeploy — `NEXT_PUBLIC_*` is baked in at build
time, so changing them needs a new build, not a restart:

```
NEXT_PUBLIC_WALLET_URL=https://api.basket.sweem.org
NEXT_PUBLIC_MARKET_DATA_URL=https://market.basket.sweem.org
NEXT_PUBLIC_PRIVY_APP_ID=...
NEXT_PUBLIC_PRIVY_POLICY_ID=...
NEXT_PUBLIC_CHAIN_ID=8453
```

`ui_origin` in `terraform.tfvars` becomes `CORS_ORIGINS` on both APIs. If the UI
is served from any other origin — a preview deployment, say — its requests are
refused until that origin is added.

## Redeploy

```bash
cd infra && make deploy     # rsync, rebuild, restart; volumes survive
make check                  # both hostnames over TLS from outside
```

## Notes

- `t3.medium`, not something smaller: a release build of the Rust executor needs
  more than 2 GB. The playbook adds 4 GB of swap regardless.
- The instance builds its own images. To keep the box tiny, build and push
  elsewhere and change the compose overlay to pull.
- The whole stack is one instance with no redundancy. That is the right size for
  a hackathon deployment and the wrong size for real money; the architecture
  survives the box dying — funds are in users' own wallets, and Postgres holds
  only positions and execution records, which are reconstructible from chain.
