-- TRUST EPIC · T1 (tamper-evident run log). #263 gave each run_task a legible detail row
-- (summary/tokens/duration), but run_tasks is MUTABLE — it's UPSERTed by (run_id, idx), so a
-- task's row transitions queued→running→completed IN PLACE. You cannot hash-chain a row that
-- gets rewritten. A tamper-evident audit needs an APPEND-ONLY event stream: every lifecycle
-- transition (and, later, verify-gate verdicts + approval decisions) is a new immutable row,
-- chained prev_hash → hash. run_tasks stays as the current-state projection; run_events is the
-- ledger the trust gates record into.
--
-- CHAIN SCOPE = per (run_id, daemon_id). The fleet (0041) lets MULTIPLE daemons claim tasks from
-- the SAME run concurrently, so a single linear per-run chain would be raced by concurrent
-- producers. Instead each daemon keeps its OWN sequential chain (it processes its claimed tasks
-- in a loop — its own event order is well-defined). The run's full audit is the merge of the
-- per-daemon chains (order by created_at; each event carries task_idx). Tamper-evidence holds
-- within each chain: no event can be inserted / reordered / deleted without breaking that
-- daemon's prev_hash → hash linkage, and any reader can verify it end-to-end.
--
-- WHO COMPUTES: the DAEMON (authority on its own execution). hash = sha256_hex(prev_hash + "\n" +
-- canonical_json({seq, kind, task_idx, payload})). daemon_id is a STORAGE/uniqueness concern, not
-- part of the hashed content (the daemon doesn't know its own UUID — the server derives it from
-- the device token on append). The server ENFORCES CONTINUITY on insert: for (run_id, daemon_id),
-- the new row's seq must be last_seq+1 (or 0 for the first) and its prev_hash must equal the last
-- stored hash (or '' for the first). A gap, a fork, or a mismatched prev_hash is rejected.
--
-- INVARIANT (identical to runs/run_tasks): the control plane only ever holds DATA. payload is the
-- worker's own report (summary/status/branch — never a path/argv), and only the OWNING daemon
-- (service role, device token) inserts, after the continuity check. No authenticated writes;
-- team members read via the parent run. Append-only: no UPDATE, no DELETE (enforced below).

create table public.run_events (
  id         uuid primary key default gen_random_uuid(),
  run_id     uuid not null references public.runs(id) on delete cascade,
  daemon_id  uuid not null references public.daemons(id) on delete cascade,
  seq        int  not null,                    -- 0-based, monotonic within (run_id, daemon_id)
  prev_hash  text not null,                    -- '' for the genesis event (seq 0)
  hash       text not null,                    -- sha256_hex(prev_hash + "\n" + canonical_json)
  kind       text not null,                    -- 'queued'|'running'|'completed'|'failed'|'blocked'
                                               -- (extensible: later 'verify'|'gate'|'decision')
  task_idx   int,                              -- the run_tasks.idx this event is about (null = run-level)
  payload    jsonb not null default '{}',      -- the hashed report body (team's own DATA)
  created_at timestamptz not null default now(),
  unique (run_id, daemon_id, seq),             -- one event per position in a daemon's chain
  unique (run_id, daemon_id, hash)             -- a hash can't repeat within a chain (replay guard)
);
alter table public.run_events enable row level security;

-- Read: anyone who can read the PARENT run — the same wall as run_tasks (0037), joined through
-- run_id. No cross-team leakage: an event is only visible to those who can already see its run.
create policy "run_events: readable via parent run"
  on public.run_events for select to authenticated
  using (exists (
    select 1 from public.runs r
    where r.id = run_events.run_id
      and (public.is_org_member(r.org_id) or r.created_by = auth.uid())
  ));

-- No authenticated INSERT policy: the owning daemon (device token) appends via the service role
-- through …/run/[id]/events, which authorizes org membership + runs the continuity check first.
-- Authenticated users get SELECT only (no UPDATE/DELETE policy = RLS denies both), so a team
-- member can never rewrite the ledger.
--
-- TAMPER-EVIDENCE is the HASH CHAIN, not a DB write-lock. We deliberately do NOT add rules/
-- triggers to block UPDATE/DELETE: the sole writer is the service role, which BYPASSES RLS, and a
-- service-key compromise is total anyway (it could drop the table). A write-block would also break
-- the legitimate ON DELETE CASCADE from runs/daemons. Instead, any insert/edit/reorder/delete of a
-- committed event is DETECTABLE by re-verifying the chain (each prev_hash must equal the prior
-- row's hash, seq contiguous) — that's the guarantee we actually offer and can enforce on read.

create index run_events_run on public.run_events (run_id, created_at);
create index run_events_chain on public.run_events (run_id, daemon_id, seq);
