-- The venue rate is denominated in the asset, so a cbBTC position earning 2.8%
-- still loses money if BTC falls. Recording the entry price is what lets the
-- price move be reported next to the yield instead of hidden inside it.
-- 0 means unknown (rows written before this column existed).
ALTER TABLE positions
    ADD COLUMN IF NOT EXISTS entry_price_usd DOUBLE PRECISION NOT NULL DEFAULT 0;
