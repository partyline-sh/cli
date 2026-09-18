-- EPIC O — O.3 (worker → RUN STORE, per-TASK lifecycle). Child of 0036_runs: a run is a whole
-- worklist; `run_tasks` is the per-item lifecycle the status board (O.4) reads. crank
-- self-reports each task's transitions (queued → running → done/failed/blocked) to the API as
-- it processes the worklist — the daemon spawns crank blocking/detached and can't watch it live
-- (that's a later slice), so the worker is the reporter.
--
-- INVARIANT (identical to runs): the control plane only ever holds DATA. `task` is the team's
-- own worklist string (never a path/argv), and only the OWNING daemon (service role, device
-- token) writes these rows — exactly like `runs`. No authenticated writes; team members read.

create table public.run_tasks (
  id         uuid primary key default gen_random_uuid(),
  run_id     uuid not null references public.runs(id) on delete cascade,
  idx        int not null,                    -- task order within the worklist (0-based)
  task       text not null,                   -- the task string (team's own DATA)
  status     text not null default 'queued'
             check (status in ('queued', 'running', 'blocked', 'done', 'failed')),
  branch     text,                            -- the reviewable branch crank prepared for this task
  detail     text,                            -- worker note / failure reason
  started_at timestamptz,
  ended_at   timestamptz,
  created_at timestamptz not null default now(),
  unique (run_id, idx)
);
alter table public.run_tasks enable row level security;

-- Read: anyone who can read the PARENT run. Gated by the run's org (is_org_member) OR the run's
-- creator — the same wall as runs (0036), joined through run_id. No cross-team leakage: a task
-- row is only visible to those who can already see its run.
create policy "run_tasks: readable via parent run"
  on public.run_tasks for select to authenticated
  using (exists (
    select 1 from public.runs r
    where r.id = run_tasks.run_id
      and (public.is_org_member(r.org_id) or r.created_by = auth.uid())
  ));

-- No authenticated INSERT/UPDATE policy: the owning daemon (device token) upserts task rows with
-- the service role as it processes the worklist — exactly like runs. Team members never write.

create index run_tasks_run on public.run_tasks (run_id);
