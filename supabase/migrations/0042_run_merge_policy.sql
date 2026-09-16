-- EPIC O — #77 slice 3 (per-run MERGE POLICY, decision #86). The operator chooses, per run, what
-- happens to each task's reviewable branch after the worker commits it:
--   'manual' (default) — leave the branch; a human merges. Upholds "proposals, never pushes".
--   'pr'              — crank pushes the branch + opens a PR; a human merges.
--   'auto'            — crank pushes + opens a PR + enables GitHub auto-merge (GitHub merges when
--                       the repo's required checks pass). Opt-in; unattended writes to main.
-- Default 'manual' keeps the safe posture; pr/auto are a deliberate per-run choice at enqueue.
alter table public.runs
  add column merge_policy text not null default 'manual'
    check (merge_policy in ('manual', 'pr', 'auto'));
