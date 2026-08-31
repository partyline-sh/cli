-- EPIC O — #81 slice 3b (per-run token budget). Gives a run an OPTIONAL ceiling so an
-- unattended (daemon) run actually hits the wall and PAUSES (slice 2's needs_approval) instead
-- of running unbounded. The value is a run-owned int the daemon feeds to crank as --max-tokens
-- (O.5); crank pauses → the daemon maps budgetPauseExit → needs_approval. NULL or 0 = unbounded
-- (the existing behaviour — every already-queued run keeps running without a ceiling).
alter table public.runs add column max_tokens integer;
