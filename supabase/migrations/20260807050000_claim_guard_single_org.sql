-- AUTO-CLAIM GUARD — never move someone who already has a real workspace.
--
-- 20260807040000 auto-claims pending invites on verification. Correct for the case it was built
-- for (an invitee whose only org is the empty personal one the signup trigger made), and DANGEROUS
-- one step outside it: `myOrg` resolves to the NEWEST membership, so adding a second org silently
-- changes which workspace a working user resolves into. That is #662 exactly — projects in org A,
-- resolution flips to org B, planning 404s "project not found" — and the epic
-- docs/epics/one-org-per-user.md exists because that ambiguity keeps costing us.
--
-- The production table in that epic is the proof: matthew@acrretail.com already sits in ACR *and*
-- a personal org. An unguarded claim would have flipped an account like that into a third org, in
-- a backfill loop, silently, for everyone at once.
--
-- THE RULE. Auto-claim only PLACES someone who has nowhere real to be:
--   · no membership in any org that holds work (projects/runs/threads/sessions/parties), and
--   · no membership in an org with other members (someone else's team is a real place).
-- Anyone else keeps their invite PENDING — it stays visible and acceptable by hand, where the
-- consequence of switching orgs is a decision a human makes with their eyes open.
--
-- Explicit acceptance (accept_org_invite, the link) is deliberately NOT guarded: clicking the link
-- IS the eyes-open decision. This guard governs only what happens automatically.

create or replace function public.claim_pending_invites_for(uid uuid)
returns int language plpgsql security definer set search_path = public as $$
declare
  inv       org_invites;
  claimed   int := 0;
  joined    uuid[] := '{}';
  addr      text;
  stray     uuid;
  has_real  boolean;
begin
  select lower(u.email) into addr from auth.users u
    where u.id = uid and u.email_confirmed_at is not null;
  if addr is null then
    return 0;  -- unverified: the security gate, unchanged
  end if;

  -- Does this account already belong somewhere real?
  select exists (
    select 1 from org_members m
    join orgs o on o.id = m.org_id
    where m.user_id = uid
      and (
        exists (select 1 from org_members m2 where m2.org_id = o.id and m2.user_id <> uid)
        or exists (select 1 from projects p where p.org_id = o.id)
        or exists (select 1 from runs     r where r.org_id = o.id)
        or exists (select 1 from threads  t where t.org_id = o.id)
        or exists (select 1 from sessions s where s.org_id = o.id)
        or exists (select 1 from parties pa where pa.org_id = o.id)
      )
  ) into has_real;
  if has_real then
    return 0;  -- leave the invite pending: moving them is a human's call, not a trigger's
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

  -- Reclaim the empty personal org, exactly as accept_org_invite does.
  for stray in
    select o.id from orgs o
    join org_members me on me.org_id = o.id and me.user_id = uid
    where not (o.id = any (joined))
      and not exists (select 1 from org_members m2 where m2.org_id = o.id and m2.user_id <> uid)
      and not exists (select 1 from projects p where p.org_id = o.id)
      and not exists (select 1 from runs     r where r.org_id = o.id)
      and not exists (select 1 from threads  t where t.org_id = o.id)
      and not exists (select 1 from sessions s where s.org_id = o.id)
      and not exists (select 1 from parties pa where pa.org_id = o.id)
  loop
    delete from orgs where id = stray;
  end loop;

  return claimed;
end $$;
