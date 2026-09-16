-- partyline 0014_pending_invites: let an invitee SEE and accept their own team
-- invites in-app (the invite link gets lost in spam). org_invites RLS is admin-only,
-- so a regular invitee can't read invite rows — this security-definer RPC returns
-- ONLY the pending invites addressed to the caller's CONFIRMED email (H6: verified-
-- email gate). No enumeration: you only ever see invites sent to your own email.

create or replace function public.my_pending_invites()
returns table (org_id uuid, org_name text, role text, token text)
language sql security definer stable set search_path = public as $$
  select i.org_id, o.name, i.role, i.token
  from org_invites i
  join orgs o on o.id = i.org_id
  join auth.users u on u.id = auth.uid()
  where i.status = 'pending'
    and u.email_confirmed_at is not null          -- only verified emails match
    and lower(i.email) = lower(u.email)
  order by i.created_at;
$$;

grant execute on function public.my_pending_invites() to authenticated;
