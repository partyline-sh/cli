-- me_with_plan() has been BROKEN SINCE 20260728070000 — it still joins a column that migration
-- dropped, so every call errors and the profile comes back empty for EVERY user.
--
-- The function (0017) reads:
--     left join public.orgs o on o.personal = true and o.created_by = auth.uid()
-- and 20260728070000_one_org_per_user.sql line 69 did:
--     alter table public.orgs drop column if exists personal;
--
-- A `security definer` SQL function is not re-planned until it runs, so nothing failed at migration
-- time; it fails at every call, forever after. GET /api/v1/me does `const { data } = await
-- rpc("me_with_plan")` and IGNORES the error, so `data` is null and the route falls back to a
-- minimal {email} shape. Downstream that renders as: an empty handle and display name on Settings →
-- Profile, a blank `id:` and "(not set)" from `ptln whoami`, and — because the form posts what it
-- read — a Save that appears to do nothing. Ten days of "my profile won't save", from one stale join.
--
-- (The missing profiles-INSERT policy fixed in 20260807060000 was real and worth closing, but it
-- was NOT this. An empty handle looked like a missing row only because the read was erroring.)
--
-- Plan now comes from THE org — one per user (docs/epics/one-org-per-user.md). Where two memberships
-- still exist, take the NEWEST, which is exactly what myOrg (lib/api/orgs.ts) does; resolving the
-- account's plan and its workspace differently is how #662 happened.
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
  left join lateral (
    select o2.plan, o2.plan_status, o2.seats
    from public.org_members m
    join public.orgs o2 on o2.id = m.org_id
    where m.user_id = auth.uid()
    order by m.created_at desc
    limit 1
  ) o on true
  where p.id = auth.uid();
$$;
revoke all on function public.me_with_plan() from public, anon;
grant execute on function public.me_with_plan() to authenticated;
