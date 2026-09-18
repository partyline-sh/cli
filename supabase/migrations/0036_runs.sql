-- EPIC O — O.2 (reconcile a web-enqueued run-profile → drive crank). Builds on 0024
-- (launch_requests) + 0026 (threads). This is the RUN store's queue/lifecycle-at-the-RUN
-- granularity — a run = a whole worklist. Per-TASK lifecycle (branch/PR, task ids, the
-- status board's cards) lands in O.3 as a `run_tasks` child table; it is intentionally NOT
-- here.
--
-- INVARIANT, restated (identical to launch_requests): the control plane only ever holds a
-- REFERENCE — a project LABEL + thread + tasks + preset — never a path or a command. A label
-- becomes a runnable crank invocation only inside the daemon, against its OWN local registry,
-- via resolveRun (run_profile.go). Nothing here stores an absolute path or an argv. Tasks are
-- DATA (the daemon writes them to a worklist file crank reads) — never argv.

-- A run: one "run this worklist on that daemon in this thread" request. org_id is the hard
-- team wall (mirrors threads.org_id + launch_requests' party→org scoping): a run can never
-- cross teams. Service-role writes every status transition; team members read it.
create table public.runs (
  id            uuid primary key default gen_random_uuid(),
  org_id        uuid not null references public.orgs(id) on delete cascade,
  daemon_id     uuid not null references public.daemons(id) on delete cascade,
  project_label text not null,
  thread_id     uuid not null references public.threads(id) on delete cascade,
  preset        text not null default 'spec' check (preset in ('spec', 'chat', 'build')),
  tasks         jsonb not null,                 -- the worklist: an array of task strings (DATA)
  status        text not null default 'queued'
                check (status in ('queued', 'accepted', 'declined', 'running', 'done', 'failed', 'killed')),
  detail        text,                            -- decline note / failure reason
  created_by    uuid references auth.users(id) on delete set null,
  decided_at    timestamptz,
  created_at    timestamptz not null default now()
);
alter table public.runs enable row level security;

-- Read: team members of the run's org, plus the creator (mirrors threads.read + the
-- launch_requests party-members-read policy). No cross-team leakage — the org_id wall.
create policy "runs: team members read"
  on public.runs for select to authenticated
  using (public.is_org_member(org_id) or created_by = auth.uid());

-- No authenticated INSERT/UPDATE policy: the enqueue endpoint (a team member's login session)
-- inserts the `queued` row with the service role after authorizing, and the owning daemon
-- (device token) drives every status transition with the service role — exactly like
-- launch_requests. Team members never write this table directly.

-- The daemon polls this via its stream: queued runs addressed to it.
create index runs_daemon_queued on public.runs (daemon_id) where status = 'queued';
create index runs_org_created on public.runs (org_id, created_at desc);
create index runs_thread on public.runs (thread_id);
