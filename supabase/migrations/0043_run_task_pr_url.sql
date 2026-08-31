-- EPIC O — #212 (glass box). Capture the PR a task's merge_policy (pr/auto, slice 3) opened, so the
-- run detail can deep-link to it. `claimed_by` (which daemon worked the task) already exists from
-- 0041; this adds the PR link. Written by the owning/fleet daemon via the per-task report route.
alter table public.run_tasks add column pr_url text;
