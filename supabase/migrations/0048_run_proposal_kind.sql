-- WEDGE · W2. The fix-intake agent proposes a concrete fix as a `run_proposal` message: a human in
-- the party approves it, and the approval enqueues a GATED crank run (merge_policy=pr) that opens a
-- reviewable PR. Same shape as the doc propose/approve flow, but the artifact is a run, not a doc
-- edit. The proposal body is JSON {task, acceptance, project_label} (the target daemon/project the
-- human confirms at approval time). Extend the party_messages kind allow-list — mirrors 0022 (doc).
alter table public.party_messages drop constraint if exists party_messages_kind_check;
alter table public.party_messages add constraint party_messages_kind_check
  check (kind in ('msg', 'status', 'ask', 'system', 'doc', 'run_proposal'));
