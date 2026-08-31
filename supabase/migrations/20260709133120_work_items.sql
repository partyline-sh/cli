-- Work-items planning layer (docs/epics/work-items.md). SEPARATE from runs: work_items is the
-- PLANNING tree (epic ▸ feature ▸ task); runs stays the EXECUTION record. Only a `task` work item
-- spawns a run (run_id links it); epics/features are containers whose status is a ROLLUP of their
-- descendant tasks. Additive — existing runs are untouched and never backfilled; a bare `task` with
-- parent_id NULL behaves exactly like today's flat backlog card.
--
-- The 3-level depth cap (task→feature|null, feature→epic|null, epic→null) is enforced in the API
-- layer, not here, to keep the tree mutable without recursive triggers; this table constrains only
-- kind/status/readiness and "run_id ⇒ task". org_id is the hard team wall (mirrors runs/threads).
-- Writes go through the /api/v1/work-items routes with the service role AFTER an RLS membership
-- check (same posture as runs — no authenticated write policy); team members + creator read.
--
-- First migration on the timestamp-naming convention (docs/epics/work-items.md finish-discipline):
-- sequential 00NN prefixes collided (0058) once two branches added migrations in parallel.
create table if not exists public.work_items (
  id                  uuid primary key default gen_random_uuid(),
  org_id              uuid not null references public.orgs(id) on delete cascade,
  thread_id           uuid not null references public.threads(id) on delete cascade,
  kind                text not null check (kind in ('epic', 'feature', 'task')),
  parent_id           uuid references public.work_items(id) on delete restrict,
  title               text not null,
  document            text not null default '',
  acceptance_criteria jsonb not null default '[]'::jsonb,   -- [{text, verify}]
  readiness           int not null default 0 check (readiness between 0 and 5),
  status              text not null default 'draft'
                      check (status in ('draft', 'backlog', 'in_progress', 'done', 'archived')),
  rank                double precision not null default 0,   -- order among siblings
  run_id              uuid references public.runs(id) on delete set null,  -- tasks only
  created_by          uuid references auth.users(id) on delete set null,
  created_at          timestamptz not null default now(),
  updated_at          timestamptz not null default now(),
  -- only a task may carry a run; epics/features are containers
  constraint work_items_run_only_task check (run_id is null or kind = 'task')
);
alter table public.work_items enable row level security;

-- Read: team members of the item's org, plus the creator (mirrors runs' read policy). No cross-team
-- leakage — the org_id wall. No authenticated INSERT/UPDATE/DELETE: the API routes authorize (an RLS
-- read of the thread proves org membership) then mutate with the service role, exactly like runs.
create policy "work_items: team members read"
  on public.work_items for select to authenticated
  using (public.is_org_member(org_id) or created_by = auth.uid());

create index work_items_thread on public.work_items (thread_id);
create index work_items_parent on public.work_items (parent_id);
create index work_items_org_created on public.work_items (org_id, created_at desc);
create index work_items_run on public.work_items (run_id) where run_id is not null;
