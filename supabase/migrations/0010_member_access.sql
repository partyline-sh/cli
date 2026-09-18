-- partyline 0010_member_access: viewer vs full-access seat type (#2).
-- SEPARATE from the authz role (owner/admin/billing/member) — single
-- responsibility. full = paid seat, can host + be granted typing; viewer = free,
-- watch-only. Billing counts DISTINCT full-access users (see docs/BILLING.md).
-- Enforcement (viewer can't type/host) lands in #3; this is just the data + UI.

alter table public.org_members
  add column if not exists access text not null default 'viewer'
    check (access in ('viewer', 'full'));

-- Grandfather everyone who exists today: they could already host → keep them full
-- so behaviour doesn't change when enforcement turns on at GA.
update public.org_members set access = 'full';

-- Owners are always full going forward; invited members default to 'viewer'
-- (the column default). This redefines the bootstrap trigger from 0001.
create or replace function public.org_after_insert()
returns trigger language plpgsql security definer set search_path = public as $$
begin
  insert into org_members (org_id, user_id, role, access)
  values (new.id, new.created_by, 'owner', 'full');
  return new;
end $$;
