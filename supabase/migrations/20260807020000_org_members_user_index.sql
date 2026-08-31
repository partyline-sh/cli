-- #15 follow-through. The issue's main ask — org_members(user_id) — turned out to be ALREADY FIXED
-- twice over (0015_perf_indexes_and_email_rpc.sql and 0038_perf_indexes.sql both create it; found
-- when this migration's own create hit "already exists" in the schema gate). What remains is the
-- issue's optional sibling: dashboards ask "active sessions" constantly, and a partial index keeps
-- ended sessions out of it entirely.
create index if not exists sessions_active on public.sessions (status, ended_at) where ended_at is null;
