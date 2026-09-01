-- Party (Mode 2): a coordination channel where humans + AI agents talk, started from
-- Slack or the web. A distinct, lightweight session type — NOT a terminal session and
-- NOT on the blind relay (the channel is backend-mediated HTTP). Coordination only;
-- not E2EE. See docs/PARTY.md.

create table public.parties (
  id               uuid primary key default gen_random_uuid(),
  org_id           uuid not null references public.orgs(id) on delete cascade,
  created_by       uuid not null references auth.users(id) on delete set null,
  slack_team_id    text,
  slack_channel_id text,
  slack_thread_ts  text,
  join_code        text not null unique,
  agent_token_hash text not null,                     -- sha256 of the party-scoped runner token
  status           text not null default 'open' check (status in ('open', 'closed')),
  created_at       timestamptz not null default now(),
  closed_at        timestamptz
);
alter table public.parties enable row level security;

-- Visible to org members + the creator (mirrors sessions' org-scoped read).
create policy "parties: org members read"
  on public.parties for select to authenticated
  using (public.is_org_member(org_id) or created_by = auth.uid());

-- Members create their own party in an org they belong to.
create policy "parties: members create"
  on public.parties for insert to authenticated
  with check (created_by = auth.uid() and public.is_org_member(org_id));

-- Close/update by the creator or an org admin (everything else is service-role).
create policy "parties: creator or admin update"
  on public.parties for update to authenticated
  using (created_by = auth.uid() or public.org_role(org_id) in ('owner', 'admin'));

create index parties_org on public.parties (org_id);
create index parties_slack_channel on public.parties (slack_channel_id);

-- The coordination message log (short retention — purged after the party closes + a grace
-- window). Writes are service-role only (the backend mediates all posts); authenticated
-- clients (the web view) read via RLS.
create table public.party_messages (
  id         bigint generated always as identity primary key,
  party_id   uuid not null references public.parties(id) on delete cascade,
  sender     text not null,                            -- 'user:<handle>' | 'agent:<name>'
  kind       text not null default 'msg' check (kind in ('msg', 'status', 'ask', 'system')),
  body       text not null,
  meta       jsonb not null default '{}',              -- {to:[...], ask_id, status_state}
  created_at timestamptz not null default now()
);
alter table public.party_messages enable row level security;

create policy "party_messages: read via party"
  on public.party_messages for select to authenticated
  using (exists (select 1 from public.parties p
                 where p.id = party_id
                   and (public.is_org_member(p.org_id) or p.created_by = auth.uid())));

create index party_messages_party_created on public.party_messages (party_id, created_at);

-- Live agent presence per party (which runners are connected, their role, last heartbeat).
-- Drives @addressing resolution, /partyline who, and @any round-robin. Service-role writes
-- (runner heartbeats via the backend); RLS read for the web view.
create table public.party_agents (
  party_id   uuid not null references public.parties(id) on delete cascade,
  name       text not null,
  role       text,
  last_seen  timestamptz not null default now(),
  primary key (party_id, name)
);
alter table public.party_agents enable row level security;

create policy "party_agents: read via party"
  on public.party_agents for select to authenticated
  using (exists (select 1 from public.parties p
                 where p.id = party_id
                   and (public.is_org_member(p.org_id) or p.created_by = auth.uid())));
