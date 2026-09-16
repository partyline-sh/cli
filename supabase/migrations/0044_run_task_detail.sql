-- EPIC O — #263 (run legibility). Enrich each run_task with the detail that makes a crank run
-- auditable after the fact: the worker's own summary ("what I changed / what to review"), the
-- token spend, and how long the task took. This is what was missing when a run crammed in work
-- with no legible record. Written by the owning/fleet daemon via the per-task report route
-- (org-scoped POST /api/v1/daemon/run/[id]/tasks); read by team members via the run detail page.
-- All nullable/zero-default so existing rows and lifecycle-only (queued/running) reports are fine.
alter table public.run_tasks add column summary text;
alter table public.run_tasks add column tokens int;
alter table public.run_tasks add column duration_ms int;
