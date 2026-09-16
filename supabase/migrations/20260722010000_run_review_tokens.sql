-- REVIEW token/cost accounting. The build worker (crank) self-reports per-task fresh_tokens /
-- cache_read_tokens / cost_usd on run_tasks, and the run detail sums them — but the code-REVIEW
-- agent (a one-shot, no run_tasks rows) captured nothing, so its spend was invisible. These columns
-- let the reviewer report the same three figures per review, so the run detail can show build +
-- review as ONE bill (with each review itemized, since re-reviews keep one row each).
--
-- Same semantics as the run_tasks columns: fresh_tokens = the genuinely-new spend we display,
-- cache_read_tokens = cache re-reads (a muted "+N cached" detail), cost_usd = claude's own
-- total_cost_usd. Nullable — only claude reports usage today; another engine writes null, never a
-- fake 0. Older daemons that don't send these just leave them null (deploy-before-release safe).
alter table public.run_reviews
  add column if not exists fresh_tokens      int,
  add column if not exists cache_read_tokens bigint,
  add column if not exists cost_usd          numeric(12, 6);
