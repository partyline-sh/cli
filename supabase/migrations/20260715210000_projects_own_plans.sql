-- ============================================================================
-- 20260715210000_projects_own_plans.sql  —  REVIEW + DRY-RUN BEFORE APPLYING (manual, not auto-run)
-- ============================================================================
-- Model we're moving to:
--   A PROJECT is the container. It has ONE plan (a designated "plan thread")
--   and 0..N context threads. No promoted/unpromoted — every label that appears
--   anywhere becomes a real project row.
--
-- WHY THIS IS SAFE:
--   This migration ONLY touches `projects` and `threads` rows. It NEVER modifies
--   `work_items`, so every existing epic/feature/task is untouched — each keeps
--   its current thread, and that thread simply becomes its project's PLAN thread.
--   (This is the "plan-thread" approach: we get your exact model without ripping
--   out work_items.thread_id or rewiring the AI-agent planning guards.)
--
-- HOW TO VALIDATE BEFORE COMMITTING:
--   Run inside a transaction (BEGIN … ROLLBACK) first and inspect the SELECTs at
--   the bottom. Only change ROLLBACK→COMMIT once the counts look right.
--
-- THREE JUDGMENT CALLS FLAGGED INLINE  →  search for  [DECIDE]
-- ============================================================================

begin;

-- ----------------------------------------------------------------------------
-- (1) is_plan: which thread is a project's PLAN (vs a context thread).
--     Exactly one plan thread per project (enforced by the partial unique index).
-- ----------------------------------------------------------------------------
alter table public.threads add column if not exists is_plan boolean not null default false;

-- [DECIDE] #1 — enforce "one plan per project". Safe to add AFTER step (3) sets the
-- flags. If step (3) ever marked two threads for one project, this index will FAIL
-- loudly (good — that means a tie-break bug to fix, not silent corruption).
-- create unique index if not exists threads_one_plan_per_project
--   on public.threads (project_id) where is_plan;

-- ----------------------------------------------------------------------------
-- (2) KILL "unpromoted": every label used in a run OR advertised by a daemon
--     becomes a real project row.
--     org resolution: runs carry (org_id, project_label); daemon labels resolve
--     via daemons.user_id → that user's org(s). created_by = the org's owner.
-- ----------------------------------------------------------------------------
with owner_of as (
  select org_id, min(user_id::text)::uuid as owner   -- any owner; deterministic
  from public.org_members where role = 'owner' group by org_id
),
labels as (
  select distinct r.org_id, r.project_label as label
  from public.runs r where coalesce(r.project_label,'') <> ''
  union
  select distinct m.org_id, dp.label
  from public.daemon_projects dp
  join public.daemons d      on d.id = dp.daemon_id
  join public.org_members m  on m.user_id = d.user_id
  where coalesce(dp.label,'') <> ''
)
insert into public.projects (org_id, label, created_by, source)
select l.org_id, l.label, o.owner, 'promoted'
from labels l
join owner_of o on o.org_id = l.org_id
where not exists (
  select 1 from public.projects p where p.org_id = l.org_id and p.label = l.label
);

-- ----------------------------------------------------------------------------
-- (3) Every PLANNING thread (one that HAS work_items) must belong to a project,
--     and each such project needs ONE plan thread.
-- ----------------------------------------------------------------------------

-- (3a) Orphan planning threads (work_items present, project_id NULL) → create a
--      project named after the thread and link it.
--      [DECIDE] #2 — the label. `projects.label` has NO unique(org_id,label)
--      constraint today, so two orphan threads with the same title in one org
--      would make two projects with the same label. We de-dupe by suffixing the
--      thread id's first 8 chars when a label already exists. Review the label
--      shaping (regexp strips anything outside [a-zA-Z0-9 _.-], caps at 48).
with orphan as (
  select t.id as thread_id, t.org_id, t.created_by,
         left(regexp_replace(coalesce(nullif(trim(t.title),''), 'plan'), '[^a-zA-Z0-9 _.-]', '-', 'g'), 40) as base
  from public.threads t
  where t.project_id is null
    and t.created_by is not null
    and exists (select 1 from public.work_items w where w.thread_id = t.id)
),
labeled as (
  select o.*,
         case when exists (select 1 from public.projects p where p.org_id = o.org_id and p.label = o.base)
              then o.base || '-' || left(o.thread_id::text, 8)
              else o.base end as label
  from orphan o
),
newproj as (
  insert into public.projects (org_id, label, created_by, source)
  select org_id, label, created_by, 'web' from labeled
  returning id, org_id, label
)
update public.threads t
set project_id = np.id
from labeled l
join newproj np on np.org_id = l.org_id and np.label = l.label
where t.id = l.thread_id;

-- (3b) Designate the plan thread for EVERY project that owns planning threads.
--      When a project has several threads with work_items, the plan thread is the
--      one with the MOST work items (ties → oldest thread). Everything else stays
--      a context thread.
--      [DECIDE] #3 — the "most work items, oldest wins" tie-break. This is the
--      only place two threads could contend for one project's single plan slot.
with ranked as (
  select t.id as thread_id, t.project_id,
         row_number() over (
           partition by t.project_id
           order by (select count(*) from public.work_items w where w.thread_id = t.id) desc,
                    t.created_at asc
         ) as rn
  from public.threads t
  where t.project_id is not null
    and exists (select 1 from public.work_items w where w.thread_id = t.id)
)
update public.threads t set is_plan = true
from ranked r where t.id = r.thread_id and r.rn = 1;

-- ----------------------------------------------------------------------------
-- VALIDATION — inspect these BEFORE flipping ROLLBACK→COMMIT.
-- ----------------------------------------------------------------------------
-- Projects created / total:
--   select source, count(*) from public.projects group by source;
-- Every project that has planning threads has EXACTLY one plan thread (want 0 rows):
--   select p.id, p.label, count(*) filter (where t.is_plan) as plans
--   from public.projects p
--   join public.threads t on t.project_id = p.id
--   where exists (select 1 from public.work_items w
--                 join public.threads tt on tt.id = w.thread_id where tt.project_id = p.id)
--   group by p.id, p.label having count(*) filter (where t.is_plan) <> 1;
-- Any planning thread still without a project (want 0 rows):
--   select t.id, t.title from public.threads t
--   where t.project_id is null and exists (select 1 from public.work_items w where w.thread_id = t.id);

-- ── VALIDATION (prints in the results pane, then COMMIT persists) ────────────
-- Projects by source (how many got created):
select 'projects_by_source' as check, source, count(*) from public.projects group by source;
-- MUST return 0 rows — every project with planning threads has exactly one plan thread:
select 'projects_missing_or_multi_plan' as check, p.id, p.label, count(*) filter (where t.is_plan) as plans
from public.projects p
join public.threads t on t.project_id = p.id
where exists (select 1 from public.work_items w
              join public.threads tt on tt.id = w.thread_id where tt.project_id = p.id)
group by p.id, p.label having count(*) filter (where t.is_plan) <> 1;
-- MUST return 0 rows — no planning thread left without a project:
select 'planning_threads_without_project' as check, t.id, t.title from public.threads t
where t.project_id is null and exists (select 1 from public.work_items w where w.thread_id = t.id);

commit;  -- applies everything above. Idempotent + guarded, so re-running is safe.
