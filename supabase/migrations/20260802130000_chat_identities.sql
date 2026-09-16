-- Chat transports (Epic: docs/epics/chat-transports.md) — C1, the seam.
--
-- ONE table for every chat platform's identity mapping, replacing slack_identities.
--
-- WHY NOT KEEP slack_identities: it keys on user_id as PRIMARY KEY, which permits exactly one chat
-- account per person, forever. That was invisible while Slack was the only transport and becomes a
-- hard wall the moment someone wants Slack at work and Telegram on their phone. The key has to be
-- (platform, external_user_id) — the thing that is actually unique — with user_id as an ordinary
-- indexed column that many rows may share.
--
-- WHAT THIS IS FOR: an inbound chat message carries a platform user id and nothing else. This table
-- is what turns that into a partyline user, and therefore into an org and a set of permissions. A
-- sender with NO row here is not a partyline user: their message can be SEEN (it enters the party as
-- untrusted context) but it authorizes nothing — no run, no spend, no machine access. That rule is
-- the whole authorization story for every chat transport, and it lives on this table's absence.

create table if not exists public.chat_identities (
  platform            text        not null check (platform in ('slack','telegram','discord','email')),
  external_user_id    text        not null,
  -- Workspace / guild / team the id belongs to. Null where the platform has no such scope
  -- (Telegram user ids and email addresses are globally unique; Slack ids are per-workspace).
  external_account_id text,
  user_id             uuid        not null references auth.users(id) on delete cascade,
  -- Cached for rendering, so ingest does not make a platform API call per message.
  display_name        text,
  avatar_url          text,
  created_at          timestamptz not null default now(),
  primary key (platform, external_user_id)
);

create index if not exists chat_identities_user on public.chat_identities (user_id);

alter table public.chat_identities enable row level security;

-- Reads only; all writes via service role (which bypasses RLS). A person may see which chat
-- accounts are linked to THEM and nothing about anyone else's — the link between a partyline
-- account and a personal messaging account is not org-visible information.
create policy "chat_identities: own read"
  on public.chat_identities for select to authenticated using (user_id = auth.uid());

-- Carry the existing Slack links over. ON CONFLICT DO NOTHING so re-running is safe and so a
-- hand-created row wins over the backfill.
insert into public.chat_identities (platform, external_user_id, external_account_id, user_id)
select 'slack', slack_user_id, slack_team_id, user_id
from public.slack_identities
on conflict (platform, external_user_id) do nothing;

-- slack_identities is intentionally LEFT IN PLACE. Dropping it in the same deploy that introduces
-- its replacement means a rollback has nowhere to land, and the old table is small and harmless.
-- Drop it in a later migration once the Slack code path has run on this table in production.

-- ---------------------------------------------------------------------------------------------
-- Conversation binding: NO NEW COLUMNS.
--
-- parties.source_tool / source_id already exist with a unique index on
-- (org_id, source_tool, source_id) — added for tracker links and reused by /runs/[id]/discuss to
-- make repeat clicks idempotent. A chat conversation is exactly the same shape: source_tool is the
-- platform, source_id is the channel/chat/guild-channel id, and the existing unique index makes
-- "one open party per conversation" a database guarantee rather than an application-level race.
--
-- Backfill open Slack parties onto that binding so there is ONE lookup path rather than two.
-- Guarded three ways: only open parties, only those not already carrying a source, and only where
-- the (org, 'slack', channel) triple is unique among them — a channel that somehow has two open
-- parties keeps the legacy column lookup rather than tripping the unique index mid-deploy.
update public.parties p
set source_tool = 'slack', source_id = p.slack_channel_id
where p.status = 'open'
  and p.slack_channel_id is not null
  and p.source_tool is null
  and not exists (
    select 1 from public.parties q
    where q.status = 'open'
      and q.org_id = p.org_id
      and q.slack_channel_id = p.slack_channel_id
      and q.id <> p.id
  );
