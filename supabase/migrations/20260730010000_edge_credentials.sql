-- Edge E1 phase 1 (#749): one credential model. ADDITIVE ONLY — nothing reads from here yet and
-- nothing is dropped. The deploy applies migrations BEFORE recreating containers, so this file must
-- leave the currently-running code working untouched.
--
-- Today four mechanisms authenticate at the edge (api_tokens 0001, daemons 0023,
-- party_agent_tokens 0024, a2a consult auth), each with its own hashing, lookup, rate limit and
-- revocation, across auth.ts / daemon.ts / party.ts. api_tokens in particular has NO management
-- surface: created invisibly at `ptln login`, never listed, never named, never revocable.
--
-- Phase 1 (this file): create the table.
-- Phase 2: dual-read — resolve here first, fall back to the legacy table; write to both.
-- Phase 3: backfill, then drop the legacy lookups.
-- The phases are separate DEPLOYS, not separate commits: a lookup may not be dropped in the same
-- release that stops writing it.

create table if not exists public.edge_credentials (
  id           uuid primary key default gen_random_uuid(),
  -- What kind of actor this authenticates. Determines which of org_id/user_id/subject_id is set.
  kind         text not null check (kind in ('user_cli','daemon','party_agent','org_key','trigger')),
  org_id       uuid references public.orgs(id) on delete cascade,
  user_id      uuid references auth.users(id) on delete cascade,
  -- The daemon / party / trigger this belongs to, by kind. Untyped on purpose: a FK per kind would
  -- need five nullable columns and five cascade rules for one lookup nobody does in SQL.
  subject_id   uuid,
  name         text not null,
  -- Shown in the UI and indexed for lookup + secret-scanning. NOT a secret: it is the first bytes
  -- of the credential, enough to identify which integration leaked, useless for authenticating.
  prefix       text not null,
  -- sha256 of the raw value. The raw value is returned ONCE at mint and never stored.
  hash         text not null unique,
  scopes       text[] not null default '{}',
  -- Optional narrowing. Null = whatever the scopes allow across the org.
  project_id   uuid references public.projects(id) on delete cascade,
  -- Who minted it. AUDIT ONLY — a credential never acts AS this user, or revoking someone's access
  -- would leave their key operating with their old rights.
  created_by   uuid not null references auth.users(id) on delete cascade,
  created_at   timestamptz not null default now(),
  last_used_at timestamptz,
  last_used_ip inet,
  expires_at   timestamptz,
  revoked_at   timestamptz,
  -- An org-owned credential must name its org; a personal one must name its user. Enforced here
  -- rather than in code because every edge path depends on it.
  constraint edge_credentials_owner check (
    (kind in ('org_key','trigger','daemon') and org_id is not null)
    or (kind in ('user_cli','party_agent') and user_id is not null)
  )
);

-- The hot path: resolve a presented credential. Partial index — a revoked row is never a candidate,
-- so revocation is a write, not a filter every request pays for.
create index if not exists edge_credentials_live_hash
  on public.edge_credentials (hash) where revoked_at is null;
create index if not exists edge_credentials_org on public.edge_credentials (org_id);
create index if not exists edge_credentials_user on public.edge_credentials (user_id);

alter table public.edge_credentials enable row level security;

-- Read: org members see their team's credentials, and a user sees their own. Deliberately readable
-- by any member rather than admins only — "a key exists, named X, last used Y" is the transparency
-- that makes a stale integration noticeable. The hash is never selected by any client path.
create policy "edge_credentials: org or own read"
  on public.edge_credentials for select to authenticated
  using (
    (org_id is not null and public.is_org_member(org_id))
    or user_id = auth.uid()
  );

-- Writes are service-role only. Minting has to hash a value the client never sees again, and
-- revocation is status-guarded — neither belongs in a client-issued statement.

comment on table public.edge_credentials is
  'Edge E1 (#749): one credential model for every actor that authenticates at the edge. Phase 1 — created, not yet read from.';
