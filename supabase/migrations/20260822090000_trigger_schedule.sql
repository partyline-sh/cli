-- Scheduled triggers (#147/#149) — a trigger that fires on a CLOCK as well as on a webhook.
--
-- The whole feature is a WHEN. Everything about what actually runs — project, machine, preset,
-- engine, model, merge policy, the task and the gate — already lives on this row and is already
-- chosen by an admin. A schedule adds no new authority and no second way to enqueue work: the tick
-- walks the due rows and hands each one to the SAME fire path POST /api/v1/t/<slug> uses.
--
-- ADDITIVE ONLY, and inert on every existing row: cron_expr null means "not scheduled", which is
-- every trigger that exists today. The old app runs unchanged against this schema, which is the
-- contract the deploy's migrate-then-swap window depends on.

-- ── where "9am" is ───────────────────────────────────────────────────────────────────────────────
-- A schedule reads in the TEAM's zone, not the box's. Storing UTC and calling it done means a
-- nightly report lands at 08:00 for half the year, which is how people learn to ignore it.
-- profiles.timezone (0004) is the wrong one to read: a schedule belongs to the org, and whose
-- profile would we consult — the admin who created the trigger, who may have since left?
-- Null = UTC, deliberately, so nothing has to be backfilled.
alter table public.orgs
  add column if not exists timezone text;

-- 0012_authz_hardening revoked table-wide UPDATE on orgs and re-grants columns one at a time, so a
-- column edited through the caller's own RLS client needs its grant naming here or every PATCH ends
-- in 42501. Authorization is unchanged — the orgs UPDATE policy plus the owner/admin check in the
-- handler are still the gate.
grant update (timezone) on public.orgs to authenticated;

alter table public.triggers
  -- Five-field cron. Null = not scheduled; the trigger is webhook-only and behaves exactly as it
  -- always has. Validated in the API (web/src/lib/api/schedule.ts) rather than by a CHECK: the
  -- parser is the same code that computes the next run, so a value that stores is a value that
  -- fires. A regex constraint here would accept "0 0 31 2 *" and reject nothing that matters.
  add column if not exists cron_expr text,
  -- When this trigger is next OWED a run. This is the claim token, not a log: the tick advances it
  -- inside a `next_run_at <= now()`-guarded UPDATE, so whichever caller wins the update fires and
  -- every other caller's WHERE stops matching. Same shape as 0058_auto_resume's status guard.
  add column if not exists next_run_at timestamptz,
  -- When it last actually fired on schedule. Distinct from last_fired_at, which counts webhooks too.
  add column if not exists last_run_at timestamptz,
  -- Stop the clock without disabling the trigger. `enabled = false` is the big red button and takes
  -- the webhook down with it; a schedule that misbehaves at 3am should be pausable on its own.
  add column if not exists schedule_paused boolean not null default false;

-- The firing query must read only the rows that are DUE. Without this, one tick per minute scans
-- every trigger every org has ever created, forever, to find the handful with a clock on them.
create index if not exists triggers_due
  on public.triggers (next_run_at)
  where cron_expr is not null and enabled and not schedule_paused;
