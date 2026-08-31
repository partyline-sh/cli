-- Perf: collapse per-page query fan-out into single round-trips. Each PostgREST
-- .from().select() is a separate HTTPS call to Supabase; over a cross-region link
-- that's ~55ms each. These functions do the joins inside Postgres (local, ~0ms) so
-- a page pays ONE round-trip instead of several.

-- me_with_plan: the profile + the account plan (from the personal org) in one call,
-- replacing the profiles + orgs queries in GET /api/v1/me. Security definer but
-- scoped to auth.uid(), so it only ever returns the caller's own row.
create or replace function public.me_with_plan()
returns jsonb
language sql security definer stable
set search_path = public
as $$
  select jsonb_build_object(
    'handle', p.handle,
    'display_name', p.display_name,
    'avatar_url', p.avatar_url,
    'github_username', p.github_username,
    'timezone', p.timezone,
    'quiet_start', p.quiet_start,
    'quiet_end', p.quiet_end,
    'notify_email', p.notify_email,
    'signup_notified', p.signup_notified,
    'plan', coalesce(o.plan, 'free'),
    'plan_status', o.plan_status,
    'seats', o.seats
  )
  from public.profiles p
  left join public.orgs o on o.personal = true and o.created_by = auth.uid()
  where p.id = auth.uid();
$$;
revoke all on function public.me_with_plan() from public, anon;
grant execute on function public.me_with_plan() to authenticated;

-- host_names: resolve a set of user ids to a display name (display_name → handle →
-- email) in one call, replacing the separate profiles + emails_for_users lookups in
-- the session-history host resolution. Reads auth.users + profiles, so it's
-- service_role only (same trust boundary as emails_for_users) — never exposed to
-- end users, who could otherwise harvest emails by id.
create or replace function public.host_names(ids uuid[])
returns table(id uuid, name text)
language sql security definer stable
set search_path = public, auth
as $$
  select au.id,
         coalesce(nullif(pr.display_name, ''), nullif(pr.handle, ''), au.email)
  from auth.users au
  left join public.profiles pr on pr.id = au.id
  where au.id = any(ids);
$$;
revoke all on function public.host_names(uuid[]) from public, anon, authenticated;
grant execute on function public.host_names(uuid[]) to service_role;
