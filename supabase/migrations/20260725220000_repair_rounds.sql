-- MAX REPAIR ROUNDS — a per-project cap on how many times the autonomous runner's repair loop
-- re-attempts a task after a failed verify/review gate before giving up. Today the loop is
-- hard-coded to 2 iterations in crank.go (#569); this column makes that budget configurable per
-- project so a fast-moving repo can allow more repair attempts and a strict one can allow fewer
-- (or zero, to fail fast on the first bad gate).
--
-- Project-scoped (not org) because repair budget is a per-repo cost/latency decision. NULL means
-- "use the built-in default (2)" — this keeps every existing project on today's behavior until a
-- project owner sets an explicit value from the project settings page. The CHECK bounds the value
-- to 0..5: 0 = no repair attempts (single-shot), 5 = a sane upper cap so a misconfiguration can't
-- burn unbounded agent runs.
--
-- Per-run override (runs.max_repair_rounds) is intentionally NOT included — it was explicitly
-- scoped out of v1 (project-level setting only). Inert SQL: no code reads this column yet.
--
-- Filename uses the timestamp (YYYYMMDDHHMMSS) scheme, which is the CURRENT convention: the repo
-- switched from sequential 00NN_ prefixes to timestamps at 20260709133120_work_items.sql and every
-- migration since is timestamped. Supabase orders migrations by version prefix, so a timestamp
-- correctly sorts AFTER the legacy 00NN_ files (a 00NN_ name here would sort BEFORE ~50 already-
-- applied migrations and wedge `db push`). CLAUDE.md's "NNNN_ / applied through 0043" rule is stale.
--
-- The column add is guarded (idempotent), and the CHECK is applied as a separate named constraint
-- (drop-if-exists then add) so the 0..5 bound is guaranteed even on a re-run or if the column had
-- somehow been created without it — an inline CHECK on ADD COLUMN IF NOT EXISTS would be silently
-- skipped when the column already exists.
alter table public.projects
  add column if not exists max_repair_rounds int;

alter table public.projects
  drop constraint if exists projects_max_repair_rounds_check;

alter table public.projects
  add constraint projects_max_repair_rounds_check
    check (max_repair_rounds between 0 and 5);
