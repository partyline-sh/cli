-- WORK board · drag-to-reorder the Backlog. A nullable rank the owner sets by dragging queued runs
-- into the order they want to work them. Null = "no explicit rank yet" → the board falls back to
-- created_at, so this is backward-compatible and the read is tolerant of it not being applied.
alter table public.runs add column if not exists backlog_rank double precision;
create index if not exists runs_backlog_rank on public.runs (backlog_rank) where status in ('queued', 'accepted');
