-- ============================================================================
-- 20260719120000_delete_personal_org.sql
--   REVIEW + RUN MANUALLY (Supabase SQL editor) — AND ONLY AFTER the web deploy
--   that removes personalOrgId() (fix/kill-personal-org-default). Old code
--   defaults creation + the GitHub install callback to the personal org; run
--   this first and those paths 400/error until the deploy lands. That ordering
--   mistake is exactly the 20260716140000 incident — don't repeat it.
-- ============================================================================
-- WHY: owner decision (2026-07-19, true single-org direction): me@darcyreno.com
--   keeps ONE org — "Partyline Team" (ae75d34b). The personal org "me"
--   (a75618e5) is already empty of content (consolidation 20260719090000,
--   validated all-zeros) but still holds two INTEGRATIONS that must move first:
--
--   · the GitHub App installation (147062729, account partyline-sh) — bound to
--     the personal org by the old callback. Moving it to the team org also
--     ACTIVATES the App-token PR path for runs (runs resolve the installation
--     by THEIR org, which is the team — until now they fell back to the
--     daemon's local token).
--   · a duplicate slack_installs row — both orgs are bound to the same Slack
--     workspace (T061SLA29A5); the team org keeps its own row, the personal
--     duplicate dies with the org. (Bonus: Slack event routing by team id
--     stops seeing two candidate orgs.)
--
--   Stripe note: the personal org carries stripe_customer_id cus_UeVHOgDhYIWuzq
--   (free plan, NO subscription). Deleting the org orphans that empty customer
--   on the Stripe side — harmless, noted for completeness.
--
--   Signup trigger (0001_core) fires only for NEW auth users — nothing
--   recreates a personal org for this account on next login.

begin;

-- (1) Move the GitHub App installation to the org the work actually lives in.
update public.git_host_installations
set org_id = 'ae75d34b-c530-4680-86a9-0c9e41877b8f', updated_at = now()
where org_id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01' and host = 'github';

-- (2) Drop the duplicate Slack binding (the team org keeps its own row).
delete from public.slack_installs
where org_id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01';

-- (3) The membership, then the org itself.
delete from public.org_members
where org_id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01';

delete from public.orgs
where id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01' and personal = true;

-- ----------------------------------------------------------------------------
-- VALIDATION — inspect before COMMIT.
-- ----------------------------------------------------------------------------
-- Exactly ONE membership for this user (Partyline Team, owner):
select 'my_memberships' as check, o.name, o.slug, om.role
from public.org_members om join public.orgs o on o.id = om.org_id
where om.user_id = '29dc68ad-4aa7-4036-b7ca-c274494cf4b6';

-- The GitHub App installation now rides the team org:
select 'github_install' as check, org_id, installation_id, account_login
from public.git_host_installations where host = 'github';

-- One Slack binding for the workspace:
select 'slack_installs' as check, org_id, slack_team_id from public.slack_installs;

-- The personal org is gone:
select 'personal_org_rows' as check, count(*) from public.orgs
where id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01';

commit;
