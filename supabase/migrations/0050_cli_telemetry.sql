-- CLI TELEMETRY — "how many installs are live in the wild." Two anonymous, PII-free signals that
-- complement the daemon heartbeat (which only sees logged-in installs):
--   • cli_installs — one row per anonymous install-id (a random UUID the CLI stores locally, NOT an
--     identity). Upserted by the once-daily "active" ping → true UNIQUE installs + active-in-window
--     by last_seen. This is the source of truth for install counts.
--   • cli_daily    — a compact per-day rollup of event VOLUME by (kind, version, os), for trends.
--     'active' = the daily ping; 'check' = the anonymous update-check hit.
-- Both are service-role only (no RLS policies): written by the control plane, read by the operator
-- dashboard via service-role. No absolute paths, no project names, no user identity — by construction.

create table public.cli_installs (
  install_id text primary key,                         -- random, client-generated; not an identity
  first_seen timestamptz not null default now(),
  last_seen  timestamptz not null default now(),
  version    text,
  os         text
);
alter table public.cli_installs enable row level security;
create index cli_installs_last_seen on public.cli_installs (last_seen desc);

create table public.cli_daily (
  day     date   not null,
  kind    text   not null,               -- 'active' (daily ping) | 'check' (update-check hit)
  version text   not null default '',
  os      text   not null default '',
  count   bigint not null default 0,
  primary key (day, kind, version, os)
);
alter table public.cli_daily enable row level security;

-- Atomic per-day increment (upsert). Called service-role from the telemetry + version endpoints.
create or replace function public.bump_cli_daily(p_day date, p_kind text, p_version text, p_os text)
returns void language sql as $$
  insert into public.cli_daily (day, kind, version, os, count)
  values (p_day, p_kind, coalesce(p_version, ''), coalesce(p_os, ''), 1)
  on conflict (day, kind, version, os) do update set count = public.cli_daily.count + 1;
$$;
