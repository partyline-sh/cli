-- Admit 'rebase' to runs.preset.
--
-- The rebase job (rebase_job.go, REBASE_MIN 0.26.3) dispatches a hidden preset:"rebase" run — it is
-- the ONLY thing behind the ConflictBanner's "Rebase onto base" button. The code allowlist in
-- createQueuedRun was widened when that shipped; this CHECK was not. So every click has failed at
-- the insert with 23514 since the feature landed, and the entire automated repair path has been
-- unreachable in production the whole time.
--
-- This is the second time the same two-step has bitten (constraint #195, recorded after the review
-- preset needed exactly this). Adding a runs.preset value requires BOTH:
--   1. the allowlist in web/src/lib/api/runs.ts (createQueuedRun)
--   2. a migration widening runs_preset_check
-- Neither half works alone, and missing the second one fails at runtime, not at build or test time —
-- which is why it reached users twice.

alter table public.runs drop constraint if exists runs_preset_check;
alter table public.runs add constraint runs_preset_check
  check (preset in ('spec', 'chat', 'build', 'describe', 'review', 'rebase'));
