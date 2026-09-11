-- Sweem Basket — wallet service schema
-- Model: copy-trading. Funds live in each user's own Privy embedded wallet.
-- A basket is a published weight config; subscribers apply it to their own funds.

CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    privy_did     TEXT UNIQUE NOT NULL,
    wallet_address TEXT UNIQUE NOT NULL,
    delegated     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS baskets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    chain       TEXT NOT NULL DEFAULT 'Base',
    is_public   BOOLEAN NOT NULL DEFAULT FALSE,
    fee_bps     INT NOT NULL DEFAULT 0 CHECK (fee_bps >= 0 AND fee_bps <= 2000),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (creator_id, name)
);
CREATE INDEX IF NOT EXISTS idx_baskets_public ON baskets(is_public) WHERE is_public;

-- weights must sum to 10000 bps; enforced in the store layer, not the DB
CREATE TABLE IF NOT EXISTS basket_weights (
    basket_id  UUID NOT NULL REFERENCES baskets(id) ON DELETE CASCADE,
    asset      TEXT NOT NULL,
    weight_bps INT  NOT NULL CHECK (weight_bps > 0 AND weight_bps <= 10000),
    PRIMARY KEY (basket_id, asset)
);

CREATE TABLE IF NOT EXISTS subscriptions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    basket_id  UUID NOT NULL REFERENCES baskets(id) ON DELETE CASCADE,
    status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','exited')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, basket_id)
);

-- where a subscriber's money currently sits, per asset
CREATE TABLE IF NOT EXISTS positions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    basket_id   UUID NOT NULL REFERENCES baskets(id) ON DELETE CASCADE,
    asset       TEXT NOT NULL,
    venue_id    TEXT NOT NULL,           -- market-data venue ID: chain:project:pool
    chain       TEXT NOT NULL,
    project     TEXT NOT NULL,
    amount_usd  NUMERIC(38,6) NOT NULL DEFAULT 0,
    entry_apy   NUMERIC(10,4) NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, basket_id, asset)
);
CREATE INDEX IF NOT EXISTS idx_positions_user ON positions(user_id);

-- every executor action, for audit + the "where did my money go" view
CREATE TABLE IF NOT EXISTS executions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    basket_id   UUID REFERENCES baskets(id) ON DELETE SET NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('deposit','withdraw','rebalance','swap')),
    asset       TEXT NOT NULL,
    from_venue  TEXT,
    to_venue    TEXT,
    amount_usd  NUMERIC(38,6) NOT NULL DEFAULT 0,
    tx_hash     TEXT,
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','submitted','confirmed','failed')),
    error       TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_executions_user ON executions(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_executions_pending ON executions(status) WHERE status IN ('pending','submitted');
