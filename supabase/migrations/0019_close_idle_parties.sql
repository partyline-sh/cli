-- Party lifecycle: parties have no inherent "end" — agents and humans just drift
-- away. Without a reaper, every party ever started lingers as 'open' and clutters
-- the dashboard's "active" list. close_idle_parties() closes any open party whose
-- newest activity (latest agent heartbeat OR latest message) is older than the idle
-- window, after a short grace so a just-created party isn't reaped before anyone joins.
-- Called opportunistically on dashboard load (best-effort) + manual close still applies.
-- Closing flips status→closed (which also makes the party token inert: partyByToken
-- only resolves open parties) and stamps closed_at, so the party drops into History.

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
          ) < now() - make_interval(mins => idle_minutes)
      and coalesce(
            (select max(m.created_at) from public.party_messages m where m.party_id = p.id),
            p.created_at
          ) < now() - make_interval(mins => idle_minutes)
  )
  update public.parties p
     set status = 'closed', closed_at = now()
    from stale
   where p.id = stale.id;
  get diagnostics n = row_count;
  return n;
end $$;

grant execute on function public.close_idle_parties(int) to authenticated, service_role;

-- Let the dashboard react in real time when a party opens/closes (mirrors sessions).
-- Realtime still enforces RLS, so members only see their orgs' parties. Guarded so a
-- re-run / already-added table doesn't error.
do $$
begin
  alter publication supabase_realtime add table public.parties;
exception
  when duplicate_object then null;
  when undefined_object then null; -- publication not present in this environment
end $$;
