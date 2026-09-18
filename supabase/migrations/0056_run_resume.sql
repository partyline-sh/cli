-- Resume (Slice 2) — reclaim an interrupted crank run instead of restarting it. Both columns are
-- ENGINE-NEUTRAL: the values are opaque to the control plane; each engine's adapter fills them in.
--
--   run_tasks.resume_handle  — the engine's opaque per-task resume token (Claude Code session id
--                              today). NULL = this engine can't resume headless → the run falls back
--                              to restart-from-start (a fresh worktree). Written by crank as it works.
--   runs.resume_at           — when a paused run can proceed: a rate-limited run's quota-window reset
--                              (from the provider's signal), so the web can offer "resume at reset"
--                              and show "resets 8:24 PM". NULL = no scheduled resume.
--
-- No behavior change from the columns alone; the daemon/crank read+write them. Reference-not-command
-- holds: the handle is data the daemon uses to re-launch ITS OWN engine — never code from the web.
alter table public.run_tasks add column if not exists resume_handle text;
alter table public.runs add column if not exists resume_at timestamptz;
