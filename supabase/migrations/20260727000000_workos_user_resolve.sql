-- resolve_workos_user — map a WorkOS profile onto auth.users, creating the row on first sight.
--
-- Exists because the auth schema is NOT exposed through PostgREST and must not be. The obvious
-- alternative — adding `auth` to PGRST_DB_SCHEMAS so the app can query auth.users directly — would
-- have let any authenticated user enumerate every user's email address over PostgREST. This keeps
-- the schema hidden and gives the app exactly one narrow, auditable way in.
--
-- SECURITY DEFINER so it can touch auth.users, and granted to service_role ONLY: revoked from
-- public, anon and authenticated below. A caller that can execute this already holds the
-- service-role key, so it can do anything anyway — the point is that nobody else can.
--
-- Inserting here is also what bootstraps a tenant: handle_new_user is an AFTER INSERT trigger on
-- auth.users (0001_core.sql:87) and still fires, creating the profile row and the personal org.
create or replace function public.resolve_workos_user(
  p_workos_user_id text,
  p_email          text,
  p_full_name      text default null
)
returns uuid
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

  -- Match on workos_user_id FIRST. Matching on email alone would let a re-registered WorkOS
  -- identity silently take over an existing partyline account.
  select id into v_id from auth.users where workos_user_id = p_workos_user_id;
  if found then
    return v_id;
  end if;

  -- Then by email, so accounts that predate WorkOS adopt their provider id on first login rather
  -- than being orphaned into a duplicate.
  select id into v_id from auth.users where email = v_email;
  if found then
    update auth.users
       set workos_user_id = p_workos_user_id,
           updated_at     = now()
     where id = v_id;
    return v_id;
  end if;

  insert into auth.users (workos_user_id, email, email_confirmed_at, raw_user_meta_data)
  values (
    p_workos_user_id,
    v_email,
    -- WorkOS never hands us an unverified address, so it arrives confirmed.
    now(),
    case when coalesce(trim(p_full_name), '') = ''
         then '{}'::jsonb
         else jsonb_build_object('full_name', trim(p_full_name)) end
  )
  returning id into v_id;

  return v_id;
end $$;

revoke all on function public.resolve_workos_user(text, text, text) from public, anon, authenticated;
grant execute on function public.resolve_workos_user(text, text, text) to service_role;
