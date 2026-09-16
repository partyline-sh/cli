-- auth_user_exists — does this person ALREADY have an account on this instance?
--
-- WHY THIS EXISTS. instance_settings.allow_signups (20260828120000) has been a column and a PATCH
-- field with nothing reading it. The sign-in callback is the only place an account is ever created
-- — resolve_workos_user inserts into auth.users on first sight — so that is where the switch has to
-- be read. But the callback cannot tell "a returning user" from "a stranger" without looking, and
-- the auth schema is deliberately NOT exposed through PostgREST: adding it would let any
-- authenticated user enumerate every email address in the system.
--
-- So: one narrow, auditable question, answered by a SECURITY DEFINER function granted to
-- service_role alone. It returns a boolean and nothing else — no id, no email, no row — so a caller
-- that somehow reached it learns only what it already had to know to ask.
--
-- MATCHES THE SAME ORDER resolve_workos_user USES (provider id first, then email). If the two ever
-- disagreed, the gate would refuse someone the resolver would happily have matched — the failure
-- mode is a locked-out returning user, which is exactly the one worth avoiding.
create or replace function public.auth_user_exists(
  p_workos_user_id text,
  p_email          text
)
returns boolean
language sql
security definer
set search_path = public, auth
as $$
  select exists (
    select 1 from auth.users
     where (p_workos_user_id is not null and workos_user_id = p_workos_user_id)
        or (email = lower(trim(coalesce(p_email, ''))) and lower(trim(coalesce(p_email, ''))) <> '')
  );
$$;

revoke all on function public.auth_user_exists(text, text) from public, anon, authenticated;
grant execute on function public.auth_user_exists(text, text) to service_role;

-- user_count — how many accounts exist at all.
--
-- THE ANTI-BRICK CLAUSE. allow_signups defaults to FALSE, and it must: an instance reachable from
-- the internet with signups open is a stranger's instance. But a freshly installed self-hosted box
-- has zero users and no way to flip the switch, because flipping it requires being signed in. Left
-- alone, every self-host install would ship bricked.
--
-- So the FIRST account is always allowed, whatever allow_signups says. After that the switch rules.
--
-- Counted from auth.users rather than profiles so it cannot be fooled by a missing profile row, and
-- exposed as its own function for the same reason as above: a bare count leaks nothing.
create or replace function public.auth_user_count()
returns bigint
language sql
security definer
set search_path = public, auth
as $$
  select count(*)::bigint from auth.users;
$$;

revoke all on function public.auth_user_count() from public, anon, authenticated;
grant execute on function public.auth_user_count() to service_role;
