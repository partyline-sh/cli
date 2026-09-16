-- AUTO-CLAIM PENDING INVITES — an invited person lands in the org that invited them, whether or
-- not they ever click the link.
--
-- THE BUG THIS FIXES. Invite matthew@acrretail.com → he signs up at partyline.sh directly (or via
-- Google/GitHub) instead of following the emailed link → handle_new_user gives him a fresh PERSONAL
-- org → the invite sits 'pending' forever. He is logged in, working, with a daemon enrolled, and
-- invisible to the team that invited him: not in members, not in the fleet, not addressable. The
-- inviter sees nothing wrong; the invitee is never told there is an invite waiting. Observed on a
-- real teammate — the invite had been pending for days while both sides assumed it had worked.
--
-- WHERE THIS MUST NOT HAPPEN: at INSERT on an unverified email. Auto-joining an org purely because
-- someone TYPED an address would let anyone walk into any team by claiming its members' addresses.
-- So the claim is keyed to auth.users.email_confirmed_at — the same verified-ownership standard
-- accept_org_invite and is_session_invitee already use (#10 H6, #24). That single condition is what
-- makes an automatic join safe, so it fires on the VERIFICATION event, not on signup:
--   · OAuth (Google/GitHub) → email arrives already confirmed → the INSERT branch claims.
--   · email/password        → confirmed later → the UPDATE branch claims at that moment.
--
-- Expiry still applies (an expired invite is not silently resurrected), and role comes from the
-- invite, never from the invitee.

create or replace function public.claim_pending_invites_for(uid uuid)
returns int language plpgsql security definer set search_path = public as $$
declare
  inv     org_invites;
  claimed int := 0;
  joined  uuid[] := '{}';  -- the orgs this call placed them into; never cleanup candidates
  addr    text;
  stray   uuid;
begin
  -- Verified address only. No verified email → nothing to claim (this is the security gate).
  select lower(u.email) into addr from auth.users u
    where u.id = uid and u.email_confirmed_at is not null;
  if addr is null then
    return 0;
  end if;

  for inv in
    select * from org_invites
      where status = 'pending' and expires_at > now() and lower(email) = addr
      order by created_at
  loop
    insert into org_members (org_id, user_id, role)
      values (inv.org_id, uid, inv.role)
      on conflict do nothing;
    update org_invites set status = 'accepted', accepted_by = uid where id = inv.id;
    joined := joined || inv.org_id;
    claimed := claimed + 1;
  end loop;

  if claimed = 0 then
    return 0;
  end if;

  -- Same cleanup accept_org_invite does: drop the now-pointless personal org, but ONLY when it is
  -- provably empty and solo. A membership the user actually built something in is never touched.
  for stray in
    select o.id from orgs o
    join org_members me on me.org_id = o.id and me.user_id = uid
    where not (o.id = any (joined))     -- never the org we just placed them in
      and not exists (select 1 from org_members m2 where m2.org_id = o.id and m2.user_id <> uid)
      and not exists (select 1 from projects p where p.org_id = o.id)
      and not exists (select 1 from runs     r where r.org_id = o.id)
      and not exists (select 1 from threads  t where t.org_id = o.id)
      and not exists (select 1 from sessions s where s.org_id = o.id)
      and not exists (select 1 from parties pa where pa.org_id = o.id)
  loop
    delete from orgs where id = stray; -- cascades the membership
  end loop;

  return claimed;
end $$;

-- The trigger. Named to sort AFTER on_auth_user_created (Postgres fires same-event triggers in
-- name order), so the personal org exists before we decide whether to reclaim it.
create or replace function public.on_verified_claim_invites()
returns trigger language plpgsql security definer set search_path = public as $$
begin
  if new.email_confirmed_at is not null then
    perform public.claim_pending_invites_for(new.id);
  end if;
  return new;
end $$;

drop trigger if exists zz_on_auth_user_claim_invites on auth.users;
create trigger zz_on_auth_user_claim_invites
  after insert or update of email_confirmed_at on auth.users
  for each row execute function public.on_verified_claim_invites();

-- Caller-facing version for the web/CLI to invoke explicitly (login, `ptln setup`) — a belt to the
-- trigger's braces, and the path that fixes an account whose verification predates this migration
-- without waiting for them to re-verify. Self-scoped: it can only ever claim for auth.uid().
create or replace function public.claim_my_pending_invites()
returns int language sql security definer set search_path = public as $$
  select public.claim_pending_invites_for(auth.uid());
$$;
revoke all on function public.claim_my_pending_invites() from public;
grant execute on function public.claim_my_pending_invites() to authenticated;
-- claim_pending_invites_for takes an arbitrary uid, so it stays service-role only.
revoke all on function public.claim_pending_invites_for(uuid) from public, authenticated, anon;

-- ============================================================ backfill
-- Existing accounts whose verified email already matches a live pending invite: place them now.
-- This is the whole point — people are sitting in the wrong org TODAY.
do $$
declare u record; n int;
begin
  for u in
    select distinct usr.id
    from org_invites i
    join auth.users usr on lower(usr.email) = lower(i.email) and usr.email_confirmed_at is not null
    where i.status = 'pending' and i.expires_at > now()
  loop
    n := public.claim_pending_invites_for(u.id);
    raise notice 'auto-claimed % invite(s) for %', n, u.id;
  end loop;
end $$;
