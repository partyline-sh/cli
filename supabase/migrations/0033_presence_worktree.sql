-- E3.6 — the worktree/agent board. Presence rows learn WHERE the session runs: the
-- machine (hostname) and git branch (⎇, usually a session worktree). Identity widens so
-- the same user+engine on two machines/branches shows as two rows — that's the board.
-- Still the one-shot-ping MVP: no heartbeat, "last seen" semantics.

alter table public.thread_presence add column if not exists machine text not null default '';
alter table public.thread_presence add column if not exists branch  text not null default '';

alter table public.thread_presence drop constraint if exists thread_presence_pkey;
alter table public.thread_presence add primary key (thread_id, user_id, engine, machine, branch);
