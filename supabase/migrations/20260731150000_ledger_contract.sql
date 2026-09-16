-- Record the ledger's own contract, in the ledger's own database.
--
-- schema_migrations is created by deploy/stack/apply-migrations.sh, not by a migration, so nothing
-- in this directory has ever described what it means. After 2026-07-31 — when a row went missing and
-- wedged every deploy for two hours — that is worth writing down where the next person looks.
--
-- This migration also serves a second purpose, deliberately: it is the FIRST migration to apply
-- under the atomic apply+record change, so it is what proves that path actually runs. Every deploy
-- since the fix reported "applied 0 this run", because everything was already recorded — the changed
-- code had never once executed.
comment on table public.schema_migrations is
  'One row per applied migration, written INSIDE the migration''s own transaction (see deploy/stack/apply-migrations.sh). Apply and record must stay atomic: when they were two transactions, a process killed between them left a migration applied but unrecorded, and the next deploy replayed it and died on a bare CREATE TABLE.';
