-- Review agent: a hidden run with preset 'review' targets a finished run (runs.review_of) and posts
-- back a graded code review. createQueuedRun's allowlist already admits 'review', but the runs.preset
-- CHECK constraint did not — so the insert failed with 23514 and the endpoint 500'd. Widen the check to
-- admit 'review' (mirrors 20260709143000_run_preset_describe.sql, which did the same for 'describe').

alter table public.runs drop constraint if exists runs_preset_check;
alter table public.runs add constraint runs_preset_check
  check (preset in ('spec', 'chat', 'build', 'describe', 'review'));
