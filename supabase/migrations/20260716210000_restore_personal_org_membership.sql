-- Restore me@darcyreno's ownership of their OWN personal org "me" (slug me-29dc68).
--
-- The 20260716140000 cleanup migration deleted this user's `personal=true` org_members rows to clear
-- multi-org junk, but it also removed their legitimate membership in their own personal org. That
-- orphaned the "me" org (zero members) along with everything RLS-scoped to it — the `partyline`
-- project, its plan threads, and ~17 parties / ~60 sessions — hiding them from the user, and breaking
-- personalOrgId() (returns null) so the GitHub App install callback errored with ?github=error.
--
-- This re-inserts the one owner membership. Idempotent (on conflict do nothing). Only the user's own
-- rows — no third-party data touched.
-- GUARDED (added 2026-07-26): both ids below are literals from the PRODUCTION database, so on any
-- other database — a fresh staging box, a self-host, a CI replay — the insert hits a foreign-key
-- violation and halts the whole migration run. Measured by task #177: this was 1 of only 3 real
-- failures in a 114-migration replay against plain postgres:16.
--
-- The guard makes it a no-op wherever those rows don't exist, which is every environment except
-- prod. On prod it is already applied, so this changes nothing there.
do $$
begin
  if exists (select 1 from public.orgs where id = 'a75618e5-1d0e-4ef1-81af-93c251b8fc01')
     and exists (select 1 from auth.users where id = '29dc68ad-4aa7-4036-b7ca-c274494cf4b6') then
    insert into public.org_members (org_id, user_id, role)
    values ('a75618e5-1d0e-4ef1-81af-93c251b8fc01',  -- orgs.id where slug = 'me-29dc68'
            '29dc68ad-4aa7-4036-b7ca-c274494cf4b6',  -- auth.users.id for me@darcyreno.com
            'owner')
    on conflict (org_id, user_id) do nothing;
  else
    raise notice 'skipping: prod-specific org/user not present in this database';
  end if;
end
$$;
