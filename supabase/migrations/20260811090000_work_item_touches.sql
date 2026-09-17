-- Work items declare the files they expect to touch — the input to conflict avoidance.
--
-- Two tasks that edit the same file destroy each other. Not because either is wrong, but because
-- each is built by a separate agent from a fresh copy of main: neither can see the other's work, so
-- the second PR to merge either clobbers the first or lands in a conflict a human has to unpick.
-- The decomposition is supposed to prevent that by cutting vertical slices, but nothing in the
-- system ever CHECKED, so the failure only showed up at merge time — the most expensive moment.
--
-- The check needs a claim to check. This column is that claim: what the planner thinks the task
-- will edit, recorded at plan time, before anything is enqueued.
--
-- NULLABLE with no default, deliberately. Every existing row and every caller that says nothing
-- keeps working exactly as before — an unknown touch set overlaps NOTHING and blocks NOBODY.
-- Silence must never read as conflict, or the first thing anyone does is turn the gate off.

alter table work_items add column if not exists touches text[];

comment on column work_items.touches is
  'Repo-relative paths this task is expected to edit, e.g. {"web/src/lib/api/runs.ts","web/src/app/api"}. '
  'A directory entry means anything beneath it. This is the planner''s ESTIMATE, not a guarantee and '
  'not a lock: the builder is free to touch other files, and nothing here restricts what it may write. '
  'It exists so overlapping tasks can be flagged before they are enqueued in parallel. NULL or empty '
  'means unknown, which overlaps nothing.';
