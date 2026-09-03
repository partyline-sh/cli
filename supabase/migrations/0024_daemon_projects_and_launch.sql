-- Epic R — R2 (project mirror) + R3 (launch protocol). Builds on 0023 (daemons).
--
-- INVARIANT, restated: the control plane only ever holds LABELS, never paths or commands.
-- A label becomes a runnable command only inside the daemon, against its OWN local
-- registry, after its OWN owner confirms. Nothing here stores an absolute path or an argv.

-- R2 — advertised launch targets. Labels + preset only; the abs path stays on the device.
-- Mirrored from the daemon over its device-token connection (service-role writes).
create table public.daemon_projects (
  daemon_id  uuid not null references public.daemons(id) on delete cascade,
  label      text not null,
  preset     text not null default 'spec' check (preset in ('spec', 'chat')),
  created_at timestamptz not null default now(),
  primary key (daemon_id, label)
);
alter table public.daemon_projects enable row level security;

-- Owner can read their own advertised labels. The cross-party-member read (so a teammate's
-- "Add agent" picker can list these) lands with the R4 UI; until then, owner-only + the
-- service role (which the launch endpoint uses to validate an advertised label).
create policy "daemon_projects: owner read"
  on public.daemon_projects for select to authenticated
  using (exists (select 1 from public.daemons d where d.id = daemon_id and d.user_id = auth.uid()));

-- R3 — the launch request state machine + audit trail. One row per "Add agent" click.
-- Service-role writes every status transition; party members read it for the audit view.
-- The single-use party_join_ref is stored ONLY as a hash, with a short TTL — the raw ref
-- travels down the daemon stream once and is exchanged over HTTPS for a freshly-minted
-- party token (which is itself never persisted in the clear; see party_agent_tokens).
create table public.launch_requests (
  id             uuid primary key default gen_random_uuid(),
  party_id       uuid not null references public.parties(id) on delete cascade,
  daemon_id      uuid not null references public.daemons(id) on delete cascade,
  project_label  text not null,
  preset         text not null default 'spec',
  requested_by   uuid references auth.users(id) on delete set null,
  status         text not null default 'pending'
                 check (status in ('pending', 'accepted', 'declined', 'spawned', 'failed', 'killed')),
  detail         text,                    -- decline note / failure reason
  join_ref_hash  text,                    -- sha256 of the single-use party_join_ref
  ref_expires_at timestamptz,
  ref_used_at    timestamptz,
  decided_at     timestamptz,
  created_at     timestamptz not null default now()
);
alter table public.launch_requests enable row level security;

create policy "launch_requests: party members read"
  on public.launch_requests for select to authenticated
  using (exists (select 1 from public.parties p
                 where p.id = party_id
                   and (public.is_org_member(p.org_id) or p.created_by = auth.uid())));

create index launch_requests_daemon_pending on public.launch_requests (daemon_id) where status = 'pending';
create index launch_requests_party on public.launch_requests (party_id);

-- R3 — per-agent party tokens. A daemon-launched agent gets its OWN party-scoped token
-- (minted at join-ref exchange, returned once over HTTPS, stored hashed) instead of the
-- shared runner token — so it's individually attributable + revocable, and the shared
-- token is never replayed down a daemon channel. Resolved + written service-role only.
create table public.party_agent_tokens (
  id         uuid primary key default gen_random_uuid(),
  party_id   uuid not null references public.parties(id) on delete cascade,
  token_hash text unique not null,
  label      text,
  created_by uuid references auth.users(id) on delete set null,
  created_at timestamptz not null default now(),
  revoked_at timestamptz
);
alter table public.party_agent_tokens enable row level security;
-- no authenticated policies: only the service role resolves/writes these.
