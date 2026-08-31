-- resolve_workos_user now reports whether it CREATED the user.
--
-- The GoTrue callback fired two signup side effects — syncNewUserToLoops and
-- notifyOperatorOfSignup — that the WorkOS callback dropped. Restoring them needs a first-login
-- signal, and the callback cannot infer one: it looks identical whether the row was just inserted
-- or has existed for a year.
--
-- Returning it from the function keeps the decision where the insert happens, so there is no
-- second round trip and no race between "does this user exist" and "create them".
drop function if exists public.resolve_workos_user(text, text, text);

create or replace function public.resolve_workos_user(
  p_workos_user_id text,
  p_email          text,
  p_full_name      text default null
)
returns table (user_id uuid, created boolean)
language plpgsql
security definer
set search_path = public, auth
as $$
declare
  v_id    uuid;
  v_email text := lower(trim(p_email));
begin
  if p_workos_user_id is null or v_email = '' then
    raise exception 'workos_user_id and email are required';
  end if;

  -- workos_user_id FIRST: matching on email alone would let a re-registered WorkOS identity take
  -- over an existing partyline account.
  select id into v_id from auth.users where workos_user_id = p_workos_user_id;
  if found then
    return query select v_id, false;
    return;
  end if;

  -- Then email, so accounts predating WorkOS adopt their provider id instead of being orphaned
  -- into a duplicate. Not a signup — no side effects should fire.
  select id into v_id from auth.users where email = v_email;
  if found then
    update auth.users
       set workos_user_id = p_workos_user_id,
           updated_at     = now()
     where id = v_id;
    return query select v_id, false;
    return;
  end if;

  insert into auth.users (workos_user_id, email, email_confirmed_at, raw_user_meta_data)
  values (
    p_workos_user_id,
    v_email,
    now(), -- WorkOS never hands us an unverified address
    case when coalesce(trim(p_full_name), '') = ''
         then '{}'::jsonb
         else jsonb_build_object('full_name', trim(p_full_name)) end
  )
  returning id into v_id;

  return query select v_id, true;
end $$;

revoke all on function public.resolve_workos_user(text, text, text) from public, anon, authenticated;
grant execute on function public.resolve_workos_user(text, text, text) to service_role;
