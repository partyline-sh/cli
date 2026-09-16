-- Build-board concurrency caps — a per-team limit on how many runs the WEB dispatches at once.
-- Enforced entirely in the control plane (acceptedRuns decides how many `accepted` runs to push as
-- `go`); the daemon is unchanged and runs whatever it's handed. Two knobs:
--   max_concurrent_runs   — fleet-wide: never more than N runs executing across all the team's machines
--   max_runs_per_machine  — per daemon: never more than N on any one machine
-- NULL = unlimited (the default, so existing teams are unchanged). A set value must be >= 1.
alter table public.orgs add column if not exists max_concurrent_runs int;
alter table public.orgs add column if not exists max_runs_per_machine int;

alter table public.orgs drop constraint if exists orgs_max_concurrent_runs_positive;
alter table public.orgs add constraint orgs_max_concurrent_runs_positive
  check (max_concurrent_runs is null or max_concurrent_runs >= 1);
alter table public.orgs drop constraint if exists orgs_max_runs_per_machine_positive;
alter table public.orgs add constraint orgs_max_runs_per_machine_positive
  check (max_runs_per_machine is null or max_runs_per_machine >= 1);

-- 0012_authz_hardening revoked table-wide UPDATE on orgs and re-grants only specific columns. These
-- are edited through the owner/admin PATCH /api/v1/orgs/[slug] via the caller's RLS client, so they
-- need the column-level grant too (else "permission denied for table orgs", 42501). Authorization is
-- unchanged and still enforced twice (orgs UPDATE RLS policy + the PATCH role check).
grant update (max_concurrent_runs, max_runs_per_machine) on public.orgs to authenticated;
