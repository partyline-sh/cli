-- partyline 0015_perf: kill the GoTrue getUserById N+1 (#14) + add missing indexes (#15).

-- #15: "list my orgs" filters org_members by user_id, but the PK is (org_id, user_id)
-- so a user_id-only filter can't use it. Hit on the sidebar + /teams on every load.
create index if not exists org_members_user on public.org_members (user_id);
-- /history: ended sessions ordered by ended_at (RLS + LIMIT do most of the work today).
create index if not exists sessions_status_ended on public.sessions (status, ended_at desc);

-- #14: emails live in auth.users (not the public schema), so the app resolved them
-- with one admin.auth.admin.getUserById() PER user — a slow GoTrue REST call each,
-- N per member list + one on every bearer-token request. Replace with ONE SQL call:
-- a security-definer function that batch-returns id→email for an array of ids.
-- service_role-only (same trust boundary as the admin client that calls it today).
create or replace function public.emails_for_users(ids uuid[])
returns table (id uuid, email text)
language sql
security definer
stable
set search_path = public
as $$
  select u.id, u.email::text from auth.users u where u.id = any(ids);
$$;
revoke all on function public.emails_for_users(uuid[]) from public, anon, authenticated;
grant execute on function public.emails_for_users(uuid[]) to service_role;
