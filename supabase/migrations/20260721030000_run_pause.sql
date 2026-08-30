-- Manual run PAUSE / RESUME (crank-01). Today a running run can only finish or be cancelled — there's
-- no way to HOLD it mid-flight. This adds a `paused` status plus the same intent-signal shape as the
-- kill switch (0025 / 20260718010000): the web records the intent here, the stream delivers it to the
-- owning daemon, and the daemon — the only thing that can touch the crank child — SIGSTOPs its process
-- group (pause) or SIGCONTs it (resume). The status flip stays immediate so the board and CTAs respond
-- even when the daemon is offline; the intent columns are what make the process actually stop / continue.
--
-- `paused` is DISTINCT from `needs_approval`: needs_approval is an automatic pause on a rate-limit /
-- verify gate that resumes by RE-DISPATCHING (crank --resume). `paused` is a human hold of a LIVE
-- process that resumes by SIGCONT — the same crank picks up exactly where it froze, nothing rebuilt.

-- 1) Allow the new status. Full list mirrors 0039 (+ paused). Confirmed against 0036/0039.
alter table public.runs drop constraint if exists runs_status_check;
alter table public.runs add constraint runs_status_check
  check (status in ('queued', 'accepted', 'declined', 'running', 'done', 'failed', 'killed', 'needs_approval', 'paused'));

-- 2) The pause / resume intents the daemon polls (twin of kill_requested_at). Each action clears the
--    other so a stale intent never re-fires the wrong signal.
alter table public.runs
  add column if not exists pause_requested_at  timestamptz,
  add column if not exists pause_requested_by  uuid references auth.users(id) on delete set null,
  add column if not exists resume_requested_at timestamptz;

-- What the daemon polls: runs it owns carrying a live pause / resume intent.
create index if not exists runs_pause_pending
  on public.runs (daemon_id)
  where pause_requested_at is not null and status = 'paused';
create index if not exists runs_resume_pending
  on public.runs (daemon_id)
  where resume_requested_at is not null and status = 'running';
