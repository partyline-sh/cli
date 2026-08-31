-- ============================================================================
-- 20260716140000_cleanup_promoted_junk_and_memberships.sql
--   REVIEW + RUN MANUALLY (Supabase SQL editor). Not auto-applied on deploy.
-- ============================================================================
-- WHY: the prior migration (20260715210000_projects_own_plans) promoted every
--   daemon-advertised label into a project row. Daemons have no org — only a
--   user_id — so the promotion joined daemons → org_members, which fanned each
--   label into EVERY org that daemon's owner belongs to. For a user in N orgs,
--   each label became N project rows. Result: the planning picker filled with
--   duplicate/cross-org "projects" that own no plan and no work items.
--
-- WHAT THIS DOES (two independent, reversible-by-recreate cleanups):
--   (1) Delete promoted projects that own ZERO work items — the fan-out junk.
--       A real plan (a project whose plan thread has work items) is NEVER
--       touched. Advertised labels still work for RUNS (runs reference the
--       label string, not this row), so nothing about launching is affected.
--   (2) Remove one specific user's stray memberships (see step 2) so their
--       planning board resolves to the single org they actually work in.
--
-- SAFETY / FK behavior (verified): projects is referenced by threads.project_id,
--   parties.project_id, projects.parent_id — all ON DELETE SET NULL; and by
--   thread_projects / project_blocks — ON DELETE CASCADE. So deleting a project
--   nulls its (empty) plan thread's project_id and never RESTRICTs. work_items
--   are reached only via threads.project_id, so the guard below is exact.
--
-- Wrap in BEGIN … ROLLBACK first, read the validation SELECTs, then flip to
-- COMMIT once the counts match the dry run.
-- ============================================================================

begin;

-- ----------------------------------------------------------------------------
-- (1) Delete promoted projects that own no work items (the fan-out artifacts).
--     "Owns work" = some work_item hangs off a thread whose project_id = p.id.
-- ----------------------------------------------------------------------------
delete from public.projects p
where p.source = 'promoted'
  and not exists (
    select 1
    from public.threads t
    join public.work_items w on w.thread_id = t.id
    where t.project_id = p.id
  );

-- ----------------------------------------------------------------------------
-- (2) Remove me@darcyreno's stray memberships: this login was added to the
--     shared ACR org (already reachable as darcy@allabout) and to an empty
--     personal org. Dropping both leaves that login in only the org it actually
--     works in (darcytest), so myOrg() and the planning picker resolve there.
--     ACR, its other members, and its plan are left fully intact — this only
--     removes ONE membership row per org, never the org or anyone else.
-- ----------------------------------------------------------------------------
delete from public.org_members om
using auth.users u, public.orgs o
where om.user_id = u.id
  and u.email = 'me@darcyreno.com'
  and om.org_id = o.id
  and (o.slug = 'acr' or o.personal = true);

-- ----------------------------------------------------------------------------
-- VALIDATION — inspect before COMMIT.
-- ----------------------------------------------------------------------------
-- Remaining projects per org (promoted junk should be gone; real plans remain):
select 'projects_remaining' as check, p.org_id, p.source, count(*)
from public.projects p group by p.org_id, p.source order by p.org_id;
-- me@darcyreno's remaining memberships (want: darcytest only):
select 'my_memberships' as check, o.name, o.slug, om.role
from public.org_members om
join public.orgs o on o.id = om.org_id
join auth.users u on u.id = om.user_id
where u.email = 'me@darcyreno.com';

commit;
