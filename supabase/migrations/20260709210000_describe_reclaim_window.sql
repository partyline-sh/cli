-- Describe chats are RECLAIMABLE work, not disposable rooms. The idle reaper (0019) closes any open
-- party after 30m idle — which killed describe conversations a human meant to come back to: a closed
-- party is read-only, its token goes inert, and it can no longer even be finalized into a work item.
--
-- A describe chat closes on FINALIZE (it produced its work item); until then it's the user's WIP draft
-- they should be able to return to. So give describe a long reclaim window (14 days idle) instead of 30
-- minutes — abandoned drafts still get reaped eventually, but you have two weeks to pick one back up.
-- Everything else (regular parties, project_setup) keeps the default idle_minutes.

create or replace function public.close_idle_parties(idle_minutes int default 30)
returns int
language plpgsql
security definer
set search_path = public
as $$
declare n int;
begin
  with stale as (
    select p.id
    from public.parties p
    where p.status = 'open'
      and p.created_at < now() - interval '5 minutes'  -- grace for a fresh party
      and coalesce(
            (select max(a.last_seen) from public.party_agents a where a.party_id = p.id),
            p.created_at
          ) < now() - make_interval(mins => case when p.mode = 'describe' then 20160 else idle_minutes end)
      and coalesce(
            (select max(m.created_at) from public.party_messages m where m.party_id = p.id),
            p.created_at
          ) < now() - make_interval(mins => case when p.mode = 'describe' then 20160 else idle_minutes end)
  )
  update public.parties p
     set status = 'closed', closed_at = now()
    from stale
   where p.id = stale.id;
  get diagnostics n = row_count;
  return n;
end $$;

grant execute on function public.close_idle_parties(int) to authenticated, service_role;
