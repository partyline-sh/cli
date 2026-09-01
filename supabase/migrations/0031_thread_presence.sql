-- COMMON GROUND — lightweight presence: who has connected a CLI session to a thread. MVP is a
-- one-shot ping on each connected session start (cg-mcp boots → upsert last_connected_at), NOT a
-- live heartbeat. The web shows "who's using this context" from these rows. Writes are service-
-- role (the /presence route); this policy just lets thread members READ it.

create table if not exists public.thread_presence (
  thread_id         uuid not null references public.threads(id) on delete cascade,
  user_id           uuid not null references auth.users(id) on delete cascade,
  engine            text not null default '',
  last_connected_at timestamptz not null default now(),
  primary key (thread_id, user_id, engine)
);

alter table public.thread_presence enable row level security;

-- Readable by anyone who can read the thread (its creator, or a team member when shared) — same
-- rule as the thread + its blocks.
create policy "thread_presence: read"
  on public.thread_presence for select to authenticated
  using (exists (select 1 from public.threads t
                 where t.id = thread_id
                   and (t.created_by = auth.uid()
                        or (t.visibility = 'team' and public.is_org_member(t.org_id)))));
