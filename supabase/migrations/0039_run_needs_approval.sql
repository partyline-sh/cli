-- partyline 0039_run_needs_approval — O.81 slice 1 (thread decision #81, web-surfaced
-- pause/approve). Adds a `needs_approval` run status: an unattended run that hits a limit
-- PAUSES into a state the operator sees on the web board. THIS slice only widens the domain
-- and surfaces it read-only — the daemon transition (write needs_approval on a limit), the
-- notification, and the approve+resume action are later slices, so this status is a
-- surfaced-but-not-yet-written foundation (exactly like run_tasks.blocked was).
--
-- The status CHECK on runs (0036) is an inline column check, which Postgres auto-names
-- `runs_status_check` (table_column_check). Confirmed against 0036. If a local DB was created
-- with a differently-named constraint, the operator should confirm the name (\d public.runs)
-- before applying. Do NOT apply automatically — a human applies migrations.
alter table public.runs drop constraint if exists runs_status_check;
alter table public.runs add constraint runs_status_check
  check (status in ('queued', 'accepted', 'declined', 'running', 'done', 'failed', 'killed', 'needs_approval'));
