-- The executor now returns a per-step trace (approve → deposit, withdraw →
-- approve → deposit). Persist it: when a sequence reverts partway, the steps
-- are the only record of which half of the money actually moved.
--
-- No CHECK change: 0001 already allows 'pending', which is now also a terminal
-- outcome (receipt poll timed out) and not only the pre-submission state.
ALTER TABLE executions ADD COLUMN IF NOT EXISTS steps JSONB;
