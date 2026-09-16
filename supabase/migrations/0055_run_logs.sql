-- Run detail overhaul (crank-01) — LIVE STEP OUTPUT. The milestone ledger (run_events, 0047) is the
-- tamper-evident, hash-chained record of lifecycle TRANSITIONS. That's the wrong home for the agent's
-- actual working output: step logs are HIGH-VOLUME (many lines per task, streamed as the worker runs)
-- and would bloat the chain — every log line would become a hashed link, and re-verification would have
-- to walk thousands of rows. So logs get their OWN stream: `run_logs`, append-only but NOT hash-chained.
--
-- The two streams stay strictly separate by design:
--   run_events (0047) — LOW volume, hash-chained, tamper-evident. Milestones: queued/running/done/…
--   run_logs   (here) — HIGH volume, plain append, best-effort telemetry. The worker's stdout/steps.
--
-- Same trust boundary as run_events/run_tasks: the control plane holds only DATA (the worker's own
-- output text), and only the OWNING daemon (service role, device token) inserts, after the org-membership
-- check. No authenticated writes; team members READ via the parent run. `seq` is a monotonic ordering
-- hint assigned by the producing crank process (one process = one daemon on one run) — it is NOT a
-- security artifact (logs are explicitly not tamper-evident), just a stable sort key when a batch of
-- lines shares a created_at timestamp.

create table public.run_logs (
  id         uuid primary key default gen_random_uuid(),
  run_id     uuid not null references public.runs(id) on delete cascade,
  daemon_id  uuid not null references public.daemons(id) on delete cascade,
  task_idx   int,                                    -- the run_tasks.idx this line is about (null = run-level)
  seq        bigint not null default 0,              -- producer-assigned monotonic ordering hint (NOT chained)
  stream     text not null default 'stdout'
             check (stream in ('stdout', 'stderr', 'step')),
  body       text not null,                          -- one log line / step (the worker's own DATA)
  created_at timestamptz not null default now()
);
alter table public.run_logs enable row level security;

-- Read: anyone who can read the PARENT run — identical wall to run_events (0047) / run_tasks (0037),
-- joined through run_id. No cross-team leakage: a log line is only visible to those who can see its run.
create policy "run_logs: readable via parent run"
  on public.run_logs for select to authenticated
  using (exists (
    select 1 from public.runs r
    where r.id = run_logs.run_id
      and (public.is_org_member(r.org_id) or r.created_by = auth.uid())
  ));

-- No authenticated INSERT/UPDATE/DELETE policy: the owning daemon (device token) appends via the
-- service role through …/run/[id]/logs, which authorizes org membership first. Team members read only.

create index run_logs_run on public.run_logs (run_id, seq, created_at);

-- REALTIME (crank-01, requirement 3: no-refresh live updates). The run detail page subscribes to the
-- run's status, task progress, ledger, and logs over Supabase Realtime. Add all four tables to the
-- publication so postgres_changes are delivered (RLS still authorizes each subscriber per row — the
-- readable-via-parent-run policies above + runs' own policy gate visibility). Each add is guarded so a
-- re-run (or an environment where the publication is absent) is a no-op rather than an error.
do $$
begin
  alter publication supabase_realtime add table public.runs;
exception when undefined_object then null; when duplicate_object then null;
end $$;
do $$
begin
  alter publication supabase_realtime add table public.run_tasks;
exception when undefined_object then null; when duplicate_object then null;
end $$;
do $$
begin
  alter publication supabase_realtime add table public.run_events;
exception when undefined_object then null; when duplicate_object then null;
end $$;
do $$
begin
  alter publication supabase_realtime add table public.run_logs;
exception when undefined_object then null; when duplicate_object then null;
end $$;
