-- The executor signs via Privy's wallet ID, not the address. Nullable because
-- rows created before this migration only ever recorded the address.
ALTER TABLE users ADD COLUMN IF NOT EXISTS privy_wallet_id TEXT;
