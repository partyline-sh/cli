-- Invite-aware signup + one-membership enforcement on accept.
-- (Epic one-org-per-user — closes the gap S2 left open.)
--
-- THE GAP. S2 made signup create exactly one org, but nothing stopped a SECOND membership arriving
-- by invite: sign up through an invite link and the trigger hands you a stray org you never asked
-- for, then accepting adds the team — two memberships, which is the state this epic exists to
-- delete. The invite-collision guard blocks inviting an EXISTING account, so this was the one
-- remaining path back into the old mess.
--
-- Two changes, both at the moment the state could go wrong rather than cleaned up after:

-- 1 · Signup skips org creation when a pending invite exists for the address.
--
-- Someone signing up through an invite is signing up TO JOIN a team; the org the trigger would
-- create is a stray from the first second. The window in which they have no org is the login
-- round-trip back to the invite page (seconds) — and if the invite is revoked while they hang
-- there, the web guard now permits re-inviting an account that has no team, so the state is
-- recoverable, not a dead end.
create or replace function public.handle_new_user()
returns trigger language plpgsql security definer set search_path = public as $$
declare
  m    jsonb := coalesce(new.raw_user_meta_data, '{}'::jsonb);
  base text := coalesce(nullif(split_part(new.email, '@', 1), ''), 'user');
  uniq text := base || '-' || substr(replace(new.id::text, '-', ''), 1, 6);
begin
  insert into profiles (id, handle, display_name, avatar_url, github_username)
    values (
      new.id, uniq,
      coalesce(m->>'name', m->>'full_name', base),
      coalesce(m->>'avatar_url', m->>'picture'),
      m->>'user_name'
    );
  if not exists (
    select 1 from org_invites
    where lower(email) = lower(new.email) and status = 'pending'
  ) then
    insert into orgs (name, created_by)
      values (public.generated_org_name(), new.id); -- org trigger adds owner membership
  end if;
  return new;
end $$;

-- 2 · Accepting an invite deletes the caller's signup stub, if one exists.
--
-- Defence in depth for accounts created BEFORE their invite (grandfathered, or a guard that failed
-- open). Deletion is deliberately narrow — the stray must be:
--   · a different org than the one being joined,
--   · with the caller as its ONLY member (removing them cannot orphan teammates), and
--   · EMPTY: no projects, runs, threads, sessions or parties.
-- Anything with content or other members is left alone: silently deleting work would be a far
-- worse failure than carrying an extra membership, and myOrg's newest-first ordering already makes
-- the freshly joined team win while both exist.
create or replace function public.accept_org_invite(invite_token text)
returns uuid language plpgsql security definer set search_path = public as $$
declare
  inv   org_invites;
  stray uuid;
begin
  select * into inv from org_invites
    where token = invite_token and status = 'pending' for update;
  if not found then raise exception 'invalid or used invite'; end if;
  insert into org_members (org_id, user_id, role)
    values (inv.org_id, auth.uid(), inv.role)
    on conflict do nothing;
  update org_invites set status = 'accepted', accepted_by = auth.uid()
    where id = inv.id;

  for stray in
    select o.id from orgs o
    join org_members me on me.org_id = o.id and me.user_id = auth.uid()
    where o.id <> inv.org_id
      and not exists (select 1 from org_members m2 where m2.org_id = o.id and m2.user_id <> auth.uid())
      and not exists (select 1 from projects  p where p.org_id  = o.id)
      and not exists (select 1 from runs      r where r.org_id  = o.id)
      and not exists (select 1 from threads   t where t.org_id  = o.id)
      and not exists (select 1 from sessions  s where s.org_id  = o.id)
      and not exists (select 1 from parties   pa where pa.org_id = o.id)
  loop
    delete from orgs where id = stray; -- cascades the membership
  end loop;

  return inv.org_id;
end $$;
