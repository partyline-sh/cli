-- partyline 0006_relay_pool: control-plane-directed relay assignment.
--
-- Strategy (see docs/SCALING.md): each session is pinned to ONE relay; the control
-- plane is the director — it assigns a relay at register time, pins it on the
-- session, and joiners resolve which relay to dial. This makes horizontal scaling
-- NON-DESTRUCTIVE: adding a relay = inserting a row here; new sessions flow to it
-- with zero impact on anything already running. No inter-instance forwarding.
--
-- Applied BY HUMAN per CLAUDE.md hard rule.

-- ============================================================ relay pool
-- One row per relay instance (each a single-instance relay workload with its own
-- stable endpoint + location/AZ). Service-role only: clients never read or pick
-- from the pool — assignment happens server-side and the chosen endpoint is handed
-- back to the host (on register) and to joiners (on code resolve).
create table public.relays (
  id             text primary key,                  -- e.g. 'pppp-usw2-1'
  endpoint       text not null,                      -- host:port clients dial, e.g. 'pppp.sh:22'
  region         text not null default 'unknown',   -- deploy location, for geo-aware placement
  capacity       int  not null default 500,         -- max LIVE sessions before we stop assigning
  draining       boolean not null default false,    -- true = finish existing, accept no new
  last_heartbeat timestamptz,                        -- relay liveness ping (null = not yet reporting)
  created_at     timestamptz not null default now()
);
alter table public.relays enable row level security;
-- intentionally NO policies: deny-all for authenticated clients; service role only
-- (same pattern as device_codes). Assignment + heartbeat run server-side.

-- which relay a session is pinned to (null for legacy/planned rows)
alter table public.sessions add column relay_id text references public.relays(id);
create index sessions_relay_live on public.sessions (relay_id, status);

-- ============================================================ director
-- Pick the relay a new session should land on: not draining, heartbeating recently
-- (null heartbeat = freshly-seeded relay, treated as healthy so manual seeds work),
-- under capacity; prefer the requested region, then least-loaded by LIVE sessions.
-- security definer so the user-scoped API client can call it without read access to
-- the (service-role-only) relays table. Returns null when the pool is exhausted —
-- the caller falls back to the client's --relay default, so a missing pool never
-- hard-breaks a host.
create function public.assign_relay(want_region text default null)
returns public.relays
language sql security definer stable set search_path = public as $$
  select r.*
  from relays r
  left join (
    select relay_id, count(*)::int as live
    from sessions
    where status = 'live' and relay_id is not null
    group by relay_id
  ) load on load.relay_id = r.id
  where r.draining = false
    and (r.last_heartbeat is null or r.last_heartbeat > now() - interval '90 seconds')
    and coalesce(load.live, 0) < r.capacity
  order by (want_region is not null and r.region = want_region) desc,
           coalesce(load.live, 0) asc
  limit 1;
$$;

-- ============================================================ seed
-- Preserve current behavior: register the existing production relay.
insert into public.relays (id, endpoint, region, capacity)
  values ('pppp-1', 'pppp.sh:22', 'aws-us-west-2', 500)
  on conflict (id) do nothing;
