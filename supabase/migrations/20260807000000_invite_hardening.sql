-- INVITE HARDENING (#10 H6 + #24) — two holes in the same trust surface, closed together.
--
-- H6 (#10): session-invite access was granted by matching the JWT *email claim* to the invite's
-- stored email. That gates access on "what email does your account claim", not "what email did
-- you prove you own" — fail-open the day any auth path yields an unverified address. The fix
-- matches against auth.users.email WITH email_confirmed_at set: the authoritative, verified
-- address, not the claim. (Deliberately NOT the JWT email_verified claim — provider-dependent
-- and sometimes absent, which would lock legitimate users out.)
--
-- #24: an org-invite link was a bearer token forever — no expiry, no email binding. A forwarded
-- or leaked link let ANY logged-in account join the org as the invited role, until someone
-- noticed and revoked. Now: 14-day expiry, and acceptance requires the caller's VERIFIED email
-- to match the invited address.

-- ============================================================ H6: verified-email gate
create or replace function public.is_session_invitee(sess uuid)
returns boolean language sql security definer stable set search_path = public as $$
  select exists (
    select 1 from session_invites si
    where si.session_id = sess
      and (si.user_id = auth.uid()
           -- Email invites: match the VERIFIED address from auth.users, never the JWT claim.
           -- Seeing the session exposes its escrowed key, so this must be proof-of-ownership.
           or (si.email is not null
               and exists (select 1 from auth.users u
                           where u.id = auth.uid()
                             and u.email_confirmed_at is not null
                             and lower(u.email) = lower(si.email)))
           or (si.team_id is not null
               and exists (select 1 from team_members tm
                           where tm.team_id = si.team_id and tm.user_id = auth.uid())))
  );
$$;

-- ============================================================ #24: expiry + email binding
alter table public.org_invites
  add column if not exists expires_at timestamptz not null default (now() + interval '14 days');

-- Backfill pre-migration invites from their creation time, so an invite sent months ago does not
-- get a fresh 14 days from today.
update public.org_invites
  set expires_at = created_at + interval '14 days'
  where status = 'pending';

create or replace function public.accept_org_invite(invite_token text)
returns uuid language plpgsql security definer set search_path = public as $$
declare
  inv   org_invites;
  stray uuid;
begin
  select * into inv from org_invites
    where token = invite_token and status = 'pending' for update;
  if not found then raise exception 'invalid or used invite'; end if;

  -- Expiry first: the clearest message for the commonest failure. The row stays 'pending' so an
  -- admin can still see and re-send it.
  if inv.expires_at < now() then
    raise exception 'invite expired';
  end if;

  -- Email binding: the link alone is not enough — the accepting account must OWN the invited
  -- address (verified, from auth.users, same standard as is_session_invitee). Invites created
  -- without an email (if any exist) keep working as link-only.
  if inv.email is not null and not exists (
    select 1 from auth.users u
    where u.id = auth.uid()
      and u.email_confirmed_at is not null
      and lower(u.email) = lower(inv.email)
  ) then
    raise exception 'invite email mismatch';
  end if;

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
