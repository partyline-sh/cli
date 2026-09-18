-- Epic R — R5 kill switch. A party member requests a kill from the web; the OWNING daemon
-- is the only thing that can actually SIGTERM the child, so the request is recorded as an
-- intent here and delivered to the daemon on its stream. The daemon terminates the detached
-- process group and transitions the request to `killed` (the terminal audit state).
alter table public.launch_requests
  add column if not exists kill_requested_at timestamptz,
  add column if not exists kill_requested_by uuid references auth.users(id) on delete set null;

-- The daemon polls this: accepted/spawned requests with a kill intent it hasn't actioned.
create index if not exists launch_requests_kill_pending
  on public.launch_requests (daemon_id)
  where kill_requested_at is not null and status in ('accepted', 'spawned');
