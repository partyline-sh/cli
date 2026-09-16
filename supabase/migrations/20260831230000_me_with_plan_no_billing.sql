-- me_with_plan() broke AGAIN, the exact way its own header says it broke before:
-- 20260829140000_drop_billing_columns dropped orgs.plan / plan_status / seats, and this
-- security-definer SQL function still selected all three. Nothing failed at migration time
-- (the body is only re-planned at call time), so every call errored ("column o2.plan does
-- not exist"), every caller that ignores rpc errors got null, and Settings → Profile
-- rendered blanks over correctly-saved data — "no error, but no saves", found on the first
-- real self-hosted box.
--
-- Billing is gone from the product entirely (Stripe removed, single-workspace), so the org
-- join has nothing left to contribute: the function is now a plain profile read. 'plan' is
-- kept as a constant for any stale caller that still keys on it.
--
-- The lesson, twice paid for: a migration that drops a column MUST recreate every function
-- whose body names it. the schema gate now re-validates every SQL function body against the final schema (supabase/tests/function_bodies.sql).
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
    'plan', 'free'
  )
  from public.profiles p
  where p.id = auth.uid();
$$;
