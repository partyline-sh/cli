-- WORK board · chains (Slice 2). A chain is a sequence of linked runs that execute in order and
-- HALT on failure: a chained run only becomes eligible to run once every earlier run in its chain
-- (higher backlog_rank) is `done`. So if one fails / needs_approval, its successors never dispatch
-- until it's resolved — while UNLINKED runs (chain_id is null) and OTHER chains keep draining.
--
-- Modeled as a single nullable column, not a table: chain_id groups runs, order comes from the
-- existing backlog_rank, routing from daemon_id, blocking from status. Null = unchained (the
-- default, fully backward-compatible — an un-applied migration just means no run is ever chained).
-- The daemon never sees chain_id; the server computes eligibility and only pushes eligible runs.
alter table public.runs add column if not exists chain_id uuid;

-- Sibling lookups (all runs in one chain) drive the eligibility check on every stream poll.
create index if not exists runs_chain_id on public.runs (chain_id) where chain_id is not null;
