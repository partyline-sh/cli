-- A REAL kill for runs. Cancel used to only mark the run `killed` server-side — a state the daemon's
-- later writes bounce off — while the crank process kept running to completion on the machine. So
-- "Cancel" meant "abandon and stop listening", not "stop". That's also why a live run couldn't be sent
-- back to Backlog: an abandoned crank would keep writing and resurrect a `queued` row.
--
-- Same shape as the launch kill switch (0025): the web records the INTENT here, the stream delivers it
-- to the owning daemon, and the daemon — the only thing that can actually SIGTERM the child — kills the
-- detached process group and reports the terminal state. The status flip stays immediate so the board
-- and CTAs still respond when the daemon is offline; this just makes the process actually die.
alter table public.runs
  add column if not exists kill_requested_at timestamptz,
  add column if not exists kill_requested_by uuid references auth.users(id) on delete set null;

-- What the daemon polls: runs it owns that are in flight and carry a kill intent.
create index if not exists runs_kill_pending
  on public.runs (daemon_id)
  where kill_requested_at is not null and status in ('accepted', 'running');
