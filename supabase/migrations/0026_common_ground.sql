-- COMMON GROUND M1 — shared context across people, machines, and engines, scoped to a team.
-- A *thread* is a time-boxed, cross-functional effort that holds a feed of attributed
-- *context blocks* (the seam facts: decisions, constraints, contracts, open questions).
-- Private to its creator until explicitly shared with the team (visibility='team') — the
-- "which" boundary in §7's private-by-default model. All writes are service-role (the
-- backend mediates every change, like party_documents); RLS grants read to the creator
-- always, and to team members once shared. See docs/COMMON-GROUND.md (v1 slice §12.1–.2).

-- A thread. org_id is the hard "who" wall (a thread can never cross teams). Projects +
-- graduation come in a later slice; v1 is thread-only (docs §10 "start thread-first").
create table public.threads (
  id          uuid primary key default gen_random_uuid(),
  org_id      uuid not null references public.orgs(id) on delete cascade,
  title       text not null,
  visibility  text not null default 'private' check (visibility in ('private', 'team')),
  created_by  uuid not null references auth.users(id) on delete cascade,
  archived_at timestamptz,
  created_at  timestamptz not null default now()
);
alter table public.threads enable row level security;

-- Read: the creator always; team members only once the thread is shared. (Private by
-- default — sharing is opt-IN, never opt-out.)
create policy "threads: read"
  on public.threads for select to authenticated
  using (created_by = auth.uid()
         or (visibility = 'team' and public.is_org_member(org_id)));

create index threads_org_created on public.threads (org_id, created_at desc);

-- A context block — one attributed seam fact in a thread's feed (the "code block" of §9).
-- author is 'user:<handle>' | 'agent:<name>'; engine records which CLI produced it. status
-- tracks the block's lifecycle (open → superseded when a newer block replaces it; graduated
-- when promoted to a project doc, a later slice). supersedes_id links an update to what it
-- replaced (kept in history, never deleted) so the feed shows "was X → now Y".
create table public.context_blocks (
  id            bigint generated always as identity primary key,
  thread_id     uuid not null references public.threads(id) on delete cascade,
  kind          text not null check (kind in ('decision', 'constraint', 'contract', 'question', 'note')),
  body          text not null,
  author        text not null,                                  -- 'user:<handle>' | 'agent:<name>'
  engine        text,                                           -- claude|codex|gemini|antigravity|… (nullable)
  status        text not null default 'open'
                check (status in ('open', 'superseded', 'graduated')),
  supersedes_id bigint references public.context_blocks(id) on delete set null,
  created_by    uuid not null references auth.users(id) on delete cascade,
  created_at    timestamptz not null default now()
);
alter table public.context_blocks enable row level security;

-- Read a block iff you can read its thread (mirrors party_documents' read-via-party).
create policy "context_blocks: read via thread"
  on public.context_blocks for select to authenticated
  using (exists (select 1 from public.threads t
                 where t.id = thread_id
                   and (t.created_by = auth.uid()
                        or (t.visibility = 'team' and public.is_org_member(t.org_id)))));

-- Feed reads are thread-scoped + ordered by id (monotonic insert order = the watermark
-- cursor a later checkup slice advances).
create index context_blocks_thread on public.context_blocks (thread_id, id);
