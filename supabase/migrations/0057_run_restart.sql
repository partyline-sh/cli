-- Restart-from-scratch (board/detail CTAs). A one-shot signal: the web sets restart_requested when
-- the owner clicks "Restart" (start the run over from the beginning), the daemon reads it off the
-- accepted-run stream event and passes `--restart` to crank INSTEAD of `--resume` (fresh worktree +
-- branch per task, prior done/blocked state ignored), then the run's `running` transition clears it
-- so a stream reconnect can't re-trigger. Distinct from Continue (resume/retry), which keeps progress.
--
-- Reference-not-command holds: this is a boolean the daemon reads to choose which of ITS OWN crank
-- flags to pass — never code from the web. No behavior change from the column alone.
alter table public.runs add column if not exists restart_requested boolean not null default false;
