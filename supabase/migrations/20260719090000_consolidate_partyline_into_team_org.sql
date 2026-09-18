-- ============================================================================
-- 20260719090000_consolidate_partyline_into_team_org.sql
--   REVIEW + RUN MANUALLY (Supabase SQL editor). Not auto-applied on deploy.
-- ============================================================================
-- WHY: me@darcyreno.com holds TWO orgs, both containing a project labelled
--   `partyline`, and every label-keyed surface (plan picker default, describe's
--   label→thread resolution) flips between them:
--
--     personal "me"      a75618e5  project 203ab292 (Jul 1)   0 runs, 22 parties
--     "Partyline Team"   ae75d34b  project 92f97895 (Jul 17)  43 runs, configured
--
--   History: the 20260716140000 cleanup removed the personal membership to force
--   single-org; that orphaned the personal org's content; 20260716210000
--   restored the membership — re-creating the two-org state, after which the
--   plan picker's "newest plan thread" default landed on the resurrected
--   personal thread (59dd9799, Jul 16) instead of the real board (fa365970,
--   Jul 1, 19 work items). Last night's shaping wrote 3 mobile work items into
--   the wrong org because of exactly that default.
--
-- DECISION (owner, 2026-07-19): Partyline Team is canonical. This migration
--   folds ALL personal-org content into the team org and removes the duplicate
--   personal `partyline` project. The personal org row and membership are KEPT
--   (personalOrgId() and the GitHub App callback depend on their existence —
--   the 20260716140000 incident is the proof); it simply ends up empty.
--
-- FK safety (same shape verified for 20260716140000): projects is referenced by
--   threads.project_id / parties.project_id ON DELETE SET NULL and by
--   thread_projects / project_blocks ON DELETE CASCADE. work_items hang off
--   threads, attachments off work_items, party_activity off parties — all move
--   implicitly with their parent's org change.
--
-- Wrap in BEGIN … ROLLBACK first, read the validation SELECTs, then flip to
-- COMMIT once the counts match the dry run.
-- ============================================================================

begin;

-- ----------------------------------------------------------------------------
-- (1) Fold the stray plan into the real board: move the personal plan thread's
--     work items into the team plan thread. Tree edges (parent_id) are between
--     work items, so the hierarchy survives; only the thread pointer changes.
--     (Merged items sort wherever their rank falls — cosmetic, reorder by hand.)
-- ----------------------------------------------------------------------------
update public.work_items
set thread_id = 'fa365970-def0-4321-a8f1-630a723ef35c'   -- team plan thread "Partyline" (Jul 1, the 19-item board)
where thread_id = '59dd9799-4a01-4780-a310-79a33d61e4ef'; -- personal plan thread (Jul 16, the strays)

-- ----------------------------------------------------------------------------
-- (2) Describe parties that were anchored to the stray thread re-anchor to the
--     real one, so a reopen/re-finalize lands on the canonical board.
-- ----------------------------------------------------------------------------
update public.parties
set thread_id = 'fa365970-def0-4321-a8f1-630a723ef35c'
where thread_id = '59dd9799-4a01-4780-a310-79a33d61e4ef';

-- ----------------------------------------------------------------------------
-- (3) Archive the now-empty personal plan thread (archive, not delete — its
--     Common Ground facts and history stay reachable). Its project_id nulls
--     when the duplicate project is deleted in (6).
-- ----------------------------------------------------------------------------
update public.threads
set archived_at = now()
where id = '59dd9799-4a01-4780-a310-79a33d61e4ef';

-- ----------------------------------------------------------------------------
-- (4) Everything org-scoped in the personal org moves to the team org:
--     threads (incl. the archived one and the Common Ground smoke test),
--     parties (22), sessions (60). runs: none exist (verified 0).
-- ----------------------------------------------------------------------------
update public.threads  set org_id = 'ae75d34b-c530-4680-86a9-0c9e41877b8f'
where org_id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01';

update public.parties  set org_id = 'ae75d34b-c530-4680-86a9-0c9e41877b8f'
where org_id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01';

update public.sessions set org_id = 'ae75d34b-c530-4680-86a9-0c9e41877b8f'
where org_id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01';

-- ----------------------------------------------------------------------------
-- (5) Parties that pointed at the duplicate project re-point to the canonical
--     one (ON DELETE SET NULL would otherwise blank them in (6)).
-- ----------------------------------------------------------------------------
update public.parties
set project_id = '92f97895-1708-47cc-8356-10d326186bd3'
where project_id = '203ab292-5cd7-45a8-b78d-62062121bc02';

-- ----------------------------------------------------------------------------
-- (6) Delete the duplicate personal `partyline` project. Its document is empty
--     and no model/branch settings are set (verified) — nothing to merge.
-- ----------------------------------------------------------------------------
delete from public.projects
where id = '203ab292-5cd7-45a8-b78d-62062121bc02';

-- ----------------------------------------------------------------------------
-- VALIDATION — inspect before COMMIT.
-- ----------------------------------------------------------------------------
-- The real board absorbed the strays (expect 19 + the folded items, e.g. 22):
select 'board_items' as check, count(*) from public.work_items
where thread_id = 'fa365970-def0-4321-a8f1-630a723ef35c';

-- Exactly ONE partyline project remains, in the team org:
select 'partyline_projects' as check, id, org_id from public.projects where label = 'partyline';

-- Exactly ONE live plan thread named partyline (the picker will show one entry):
select 'live_plan_threads' as check, id, title, org_id from public.threads
where is_plan = true and archived_at is null;

-- The personal org is EMPTY of content (all zeros expected; org + membership remain):
select 'personal_org_leftovers' as check, t.tbl, t.n from (
  select 'threads'  as tbl, count(*) as n from public.threads  where org_id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01'
  union all select 'parties',  count(*) from public.parties  where org_id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01'
  union all select 'sessions', count(*) from public.sessions where org_id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01'
  union all select 'runs',     count(*) from public.runs     where org_id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01'
  union all select 'projects', count(*) from public.projects where org_id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01'
) t;

-- If any OTHER org-scoped table still references the personal org (skills,
-- notify prefs, …), it shows up here — run and eyeball before COMMIT:
--   select table_name, column_name from information_schema.columns
--   where column_name = 'org_id' and table_schema = 'public';

commit;
