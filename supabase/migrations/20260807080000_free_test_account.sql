-- FREE UP peetoose@gmail.com — a test account holding an address its owner wants to invite.
--
-- "An account can belong to only one team, so that address has to be freed up" is the invite path
-- refusing correctly (docs/epics/one-org-per-user.md). The product answer for a REAL person is an
-- email change; for a throwaway test account it is deletion, which is what this does — once, by
-- address, with guards that make it a no-op on anything that isn't actually disposable.
--
-- GUARDS, because this deletes a production account and cannot be undone:
--   · the org is removed ONLY when it is solo (no other members) and empty (no projects, runs,
--     threads, sessions, parties). Anything else and the org is left standing.
--   · orgs.created_by is `references auth.users(id)` with NO cascade, so a user cannot be deleted
--     while they own an org row — the org must go first, or the delete fails loudly rather than
--     half-completing.
--   · everything is keyed off one lookup by email. Wrong address → zero rows → nothing happens.
--
-- Idempotent: re-running after the account is gone finds nobody and does nothing, which matters
-- because this file runs on staging and prod, and on any future replica rebuild.
do $$
declare
  uid  uuid;
  org  uuid;
  kept int := 0;
begin
  select id into uid from auth.users where lower(email) = 'peetoose@gmail.com';
  if uid is null then
    raise notice 'free_test_account: no such user — nothing to do';
    return;
  end if;

  for org in select org_id from org_members where user_id = uid loop
    if exists (select 1 from org_members m where m.org_id = org and m.user_id <> uid)
       or exists (select 1 from projects p where p.org_id = org)
       or exists (select 1 from runs     r where r.org_id = org)
       or exists (select 1 from threads  t where t.org_id = org)
       or exists (select 1 from sessions s where s.org_id = org)
       or exists (select 1 from parties pa where pa.org_id = org)
    then
      kept := kept + 1;
      raise notice 'free_test_account: keeping org % — it has members or work', org;
    else
      delete from orgs where id = org;  -- cascades memberships
    end if;
  end loop;

  if kept > 0 then
    raise exception 'free_test_account: % org(s) still hold members or work — refusing to delete the user', kept;
  end if;

  -- Any pending invite addressed to them is now meaningless; clear it so the address is clean.
  delete from org_invites where lower(email) = 'peetoose@gmail.com' and status = 'pending';

  delete from auth.users where id = uid;  -- cascades profiles, org_members, and the set-null refs
  raise notice 'free_test_account: deleted user % and its empty org(s)', uid;
end $$;
