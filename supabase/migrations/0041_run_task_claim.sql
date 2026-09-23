-- EPIC O — #77 slice 1 (parallel fleet foundation: ATOMIC CLAIM). Today a run's worklist is
-- processed top-to-bottom by its single owning daemon. The fleet lets MULTIPLE workers (daemons
-- whose OWNER is a member of the run's org) chew the SAME run's tasks concurrently — so the
-- store, not a static worklist file, becomes the source of truth for "what's left." The one hard
-- correctness requirement: two workers must NEVER claim the same task. That is this migration.
--
-- INVARIANT unchanged (0036/0037): only the service role writes. The daemon claims via the
-- device-token endpoint (…/run/[id]/claim), which authorizes org membership BEFORE calling this
-- function; the function itself does no auth. Team members still only READ.

-- Who holds a task and until when. `claimed_by` = the daemon currently working it; a task with an
-- EXPIRED lease is a crashed/stalled worker's and may be reclaimed by another. ON DELETE SET NULL:
-- if a daemon is revoked/deleted its in-flight tasks orphan their claim rather than cascade-delete.
alter table public.run_tasks
  add column claimed_by       uuid references public.daemons(id) on delete set null,
  add column lease_expires_at timestamptz;

-- Atomically claim the next available task in a run. "Available" = `queued`, OR `running` with an
-- EXPIRED lease (reclaim a dead worker's task). FOR UPDATE SKIP LOCKED is the WHOLE correctness
-- guarantee: concurrent claimers each lock a DIFFERENT row (skipping any a peer already holds)
-- instead of blocking or colliding — so N workers claim N distinct tasks. Lowest idx first (stable
-- order). Returns the claimed row, or NULL when nothing is available (pool drained). SECURITY
-- DEFINER, service-role caller only (the endpoint authorizes org membership first).
create or replace function public.claim_next_task(
  p_run_id        uuid,
  p_daemon_id     uuid,
  p_lease_seconds integer default 3600
) returns public.run_tasks
language plpgsql security definer set search_path = public as $$
declare
  t public.run_tasks;
begin
  select * into t from public.run_tasks
    where run_id = p_run_id
      and (status = 'queued'
           or (status = 'running' and lease_expires_at is not null and lease_expires_at < now()))
    order by idx
    for update skip locked
    limit 1;
  if not found then
    return null;                 -- nothing queued and no expired lease to reclaim
  end if;
  update public.run_tasks
    set status           = 'running',
        claimed_by       = p_daemon_id,
        started_at       = coalesce(started_at, now()),
        lease_expires_at = now() + make_interval(secs => p_lease_seconds)
    where id = t.id
    returning * into t;
  return t;
end;
$$;

-- Speeds the claim's row-scan: the smallest queued idx per run.
create index if not exists run_tasks_claimable on public.run_tasks (run_id, idx) where status = 'queued';
