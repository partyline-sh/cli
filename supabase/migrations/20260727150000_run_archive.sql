-- Archiving a run: take a card off the board without destroying the run.
--
-- Two asks, one mechanism. "Delete this tile" (building / blocked / review) and "archive this"
-- (shipped) both mean "stop showing me this" — so both set archived_at and the board hides it.
--
-- Deliberately NOT a hard DELETE. A run owns run_events, the append-only hash-chained ledger whose
-- whole purpose is that entries cannot be removed; deleting the parent row would cascade that away
-- and quietly break the tamper-evidence claim. It would also take run_logs, run_tasks and any
-- work_items link with it. Soft-archive keeps History, the ledger and the audit trail intact, and
-- makes the action reversible — which is what lets "delete" be safe to offer on a live run.
alter table public.runs add column if not exists archived_at timestamptz;

-- The board reads "not archived" on every load, and archived rows are the minority, so a partial
-- index on the live set is both smaller and the one the planner wants.
create index if not exists runs_not_archived_idx
  on public.runs (created_at desc)
  where archived_at is null;

comment on column public.runs.archived_at is
  'Set when a run is archived off the Work board. Null = live. Never hard-delete a run: run_events is an append-only ledger.';
