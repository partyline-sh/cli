-- Email change requests (epic one-org-per-user, S4b / #720).
--
-- WHY THIS EXISTS. An account belongs to exactly one team, so an address that already owns an
-- account cannot be invited (#719). The refusal tells someone to free the address up — and until
-- now there was no way for them to do that. This table is the pending half of that flow.
--
-- WHY IT IS A TABLE AND NOT A COLUMN. The change must not apply until the NEW address is proven
-- reachable, so there is a window between request and confirmation that has to be stored somewhere,
-- and it has to be revocable from the OLD address during that window. A nullable `pending_email` on
-- profiles could hold the address but not the audit trail — and this is the one flow in the product
-- where "who asked, from where, and did anyone try to stop it" is worth keeping after the fact.
create table if not exists public.email_changes (
  id            uuid primary key default gen_random_uuid(),
  user_id       uuid not null references auth.users(id) on delete cascade,
  -- Snapshotted, not joined: the whole point is to know what it USED to be after auth.users has
  -- moved on, and to be able to warn the old address even once it is no longer the account's.
  old_email     text not null,
  new_email     text not null,
  -- Separate secrets. The confirm token goes to the NEW address and proves control of it; the
  -- cancel token goes to the OLD one and is the brake. One token for both would mean the warning
  -- email carried the power to complete the change it is warning about.
  confirm_token text not null unique,
  cancel_token  text not null unique,
  status        text not null default 'pending'
                check (status in ('pending', 'confirmed', 'cancelled', 'expired')),
  requested_ip  text,
  expires_at    timestamptz not null,
  created_at    timestamptz not null default now(),
  decided_at    timestamptz
);

-- One live request per user. A second request supersedes the first (the endpoint cancels it), so
-- this is a safety net against two half-finished changes racing to confirm.
create unique index if not exists email_changes_one_pending
  on public.email_changes (user_id) where status = 'pending';

create index if not exists email_changes_user on public.email_changes (user_id, created_at desc);

alter table public.email_changes enable row level security;

-- Read-only, own rows: Settings shows "a change to x@y is pending". Every WRITE goes through the
-- service role, because confirming rewrites auth.users and must never be reachable from a session
-- that merely holds a row id.
create policy "email_changes: owner reads own"
  on public.email_changes for select to authenticated
  using (user_id = auth.uid());

-- ============================================================ auth.users accessors
--
-- `auth.users` is outside the exposed schema, so the app reaches it only through narrow
-- security-definer functions. Both are service_role ONLY — same posture as account_user_id (#719),
-- and for a sharper reason: the second one rewrites the address an account signs in with.

-- The account's WorkOS identity, needed to move the address at the provider before moving it here.
create or replace function public.workos_id_for_user(p_user_id uuid)
returns text
language sql
security definer
set search_path = public, auth
stable
as $$
  select workos_user_id from auth.users where id = p_user_id;
$$;

-- Set the account's address. Deliberately NOT a general-purpose update:
--
--   · It refuses if any OTHER account already holds the address. auth.users has a unique index, but
--     relying on the constraint alone would surface a raw Postgres error to someone who simply lost
--     a race — and the caller cannot tell that apart from a real failure.
--   · It stamps email_confirmed_at, because reaching this point REQUIRED clicking a link sent to
--     that address. Leaving it unconfirmed would understate what we actually know.
create or replace function public.set_user_email(p_user_id uuid, p_email text)
returns void
language plpgsql
security definer
set search_path = public, auth
as $$
declare v_email text := lower(trim(p_email));
begin
  if v_email = '' then raise exception 'email is required'; end if;
  if exists (select 1 from auth.users where lower(email) = v_email and id <> p_user_id) then
    raise exception 'address already taken';
  end if;
  update auth.users
     set email              = v_email,
         email_confirmed_at = now(),
         updated_at         = now()
   where id = p_user_id;
end $$;

revoke all on function public.workos_id_for_user(uuid) from public, anon, authenticated;
revoke all on function public.set_user_email(uuid, text) from public, anon, authenticated;
grant execute on function public.workos_id_for_user(uuid) to service_role;
grant execute on function public.set_user_email(uuid, text) to service_role;
