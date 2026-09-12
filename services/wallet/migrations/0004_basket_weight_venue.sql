-- Advanced mode: a weight may name the venue it must be routed into. NULL is
-- the normal case and means "let the router choose".
ALTER TABLE basket_weights ADD COLUMN IF NOT EXISTS venue_id TEXT;
