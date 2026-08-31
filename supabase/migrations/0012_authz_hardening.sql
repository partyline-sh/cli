-- partyline 0012_authz_hardening: close the access-control findings from the
-- 2026-06-06 security review (docs/reviews/0004). HYBRID model: stay RLS-native,
-- but make genuinely-sensitive writes service-role-only and tighten permissive
-- policies. APPLIED BY HUMAN (CLAUDE.md rule) — apply BEFORE the next web deploy.
--
-- Reviewer context: the Supabase anon key is public and every web user holds a
-- session JWT, so anyone can call PostgREST directly. RLS — not the /api/v1
-- layer — is the real boundary. Each change below is exploitable today by an
-- authenticated user talking straight to PostgREST, bypassing the API.

-- ============================================================ CR1
-- Billing columns were UPDATE-able by any org admin (the "orgs: admins update"
-- policy is column-unrestricted), letting an admin self-grant plan='enterprise',
-- seats=9999, or hijack stripe_customer_id via a direct PostgREST call.
--
-- Fix: billing columns become SERVICE-ROLE-ONLY (the Stripe webhook is the sole
-- writer). In Postgres a table-level UPDATE grant overrides column-level revokes,
-- so we drop table-level UPDATE for clients and re-grant ONLY the column they
-- legitimately edit (orgs rename = name). service_role keeps full UPDATE.
revoke update on public.orgs from authenticated, anon;
grant  update (name) on public.orgs to authenticated;
-- (The "orgs: admins update" RLS policy still scopes WHICH rows; this scopes
--  WHICH columns. Both must pass. SELECT is untouched — entitlement reads work.)

-- ============================================================ CR2
-- "org_members: admins manage" insert checked the actor's role but not the
-- role VALUE being inserted → an admin could INSERT (self, role='owner') and
-- become a second owner (then exploit CR1). Forbid inserting 'owner' from a
-- client; owner is only ever set by the org_after_insert bootstrap trigger
-- (SECURITY DEFINER → runs as table owner, bypasses RLS, so it still works).
drop policy if exists "org_members: admins manage" on public.org_members;
create policy "org_members: admins manage"
  on public.org_members for insert to authenticated
  with check (public.org_role(org_id) in ('owner','admin') and role <> 'owner');
-- (There is intentionally NO client UPDATE policy on org_members — role/access
--  changes go through the service role in the API, which gates owner-protection
--  and the seat cap in code. Direct-client role escalation stays denied.)

-- ============================================================ CR3
-- "profiles: authenticated read" was USING (true) → any authenticated user could
-- scrape EVERY profile across all tenants (handle, display_name, github_username,
-- timezone, and the private notify_email added in 0007). Restrict reads to self
-- + users who share an org with the caller.
create or replace function public.shares_org(other uuid)
returns boolean language sql security definer stable set search_path = public as $$
  select exists (
    select 1 from org_members m1
    join org_members m2 on m1.org_id = m2.org_id
    where m1.user_id = auth.uid() and m2.user_id = other
  );
$$;
drop policy if exists "profiles: authenticated read" on public.profiles;
create policy "profiles: self or co-member read"
  on public.profiles for select to authenticated
  using (id = auth.uid() or public.shares_org(id));
-- FOLLOW-UP (not blocking): notify_email is still visible to co-members. To make
-- it strictly self-only, split private fields into a profiles_private table
-- (RLS USING id=auth.uid()); column grants can't do per-row, so a split is needed.

-- ============================================================ CR4 (mitigation)
-- The escrowed Noise session_key persisted on the row after a session ended
-- (the 0003 reaper only flips status), leaving live keys readable indefinitely
-- by anyone still satisfying the read policy. Null the key + relay_addr the
-- moment a session ends, and backfill existing ended rows.
create or replace function public.clear_session_key()
returns trigger language plpgsql set search_path = public as $$
begin
  if new.status = 'ended' and old.status is distinct from 'ended' then
    new.session_key := null;
    new.relay_addr  := null;
  end if;
  return new;
end $$;
drop trigger if exists sessions_clear_key on public.sessions;
create trigger sessions_clear_key before update on public.sessions
  for each row execute function public.clear_session_key();
update public.sessions set session_key = null, relay_addr = null where status = 'ended';
-- NOTE: this does NOT address that we hold the key at all (CR4 in the review).
-- For visibility='org' sessions the key is readable org-wide BY DESIGN (org-visible
-- = the org can watch = the org needs the key), so that is not narrowed here.
-- The "is it really E2EE" claim remains a PRODUCT decision (see docs/reviews/0004).

-- ============================================================ H4
-- "team_members: org admins or team lead manage" only checked the CALLER's role,
-- never that the TARGET user_id is actually a member of the team's org. An admin/
-- team-lead could add ANY user id to a team → forced enrollment + session-invite
-- email + visibility leak. Require the target to already be an org member.
create or replace function public.user_in_org(u uuid, org uuid)
returns boolean language sql security definer stable set search_path = public as $$
  select exists (select 1 from org_members where org_id = org and user_id = u);
$$;
drop policy if exists "team_members: org admins or team lead manage" on public.team_members;
create policy "team_members: org admins or team lead manage"
  on public.team_members for all to authenticated
  using (public.org_role(public.team_org(team_id)) in ('owner','admin')
         or public.is_team_lead(team_id))
  with check ((public.org_role(public.team_org(team_id)) in ('owner','admin')
               or public.is_team_lead(team_id))
              and public.user_in_org(user_id, public.team_org(team_id)));

-- ============================================================ M4
-- The Stripe webhook keys plan writes on .eq("stripe_customer_id"), which updates
-- ALL matching rows. Without a uniqueness guarantee a duplicated customer id (bad
-- manual edit / restored backup) fans a plan write across accounts. Enforce it.
create unique index if not exists orgs_stripe_customer_id_key
  on public.orgs (stripe_customer_id) where stripe_customer_id is not null;

-- ============================================================ NOT in this migration
-- These are CODE changes (web/CLI), tracked separately — this file is SQL only:
--   H1  partyline agent SSH path = no-auth RCE (Go: gate or descope)
--   H2  crypto/rand.Read return ignored on key-gen (Go: shell.go)
--   H3  Stripe webhook has no replay/ordering guard (web: webhook route)
--   H5  invite endpoint mailbomb — no per-user rate limit (web: invites)
--   H6  RLS email-claim match w/o verified-email gate (needs careful testing —
--       deliberately NOT bundled here to avoid locking users out of invites)
