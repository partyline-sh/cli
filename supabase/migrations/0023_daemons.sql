-- Epic R — remote-launch daemons (R1: transport only; no launch_requests yet, that's R3).
-- One row per enabled device. Stores ONLY the sha256 of the device-scoped token (never the
-- token), like api_tokens. Owner-only. The device token is separate from the login token so
-- a resident daemon never holds your full credential, and it's individually revocable.
create table public.daemons (
  id           uuid primary key default gen_random_uuid(),
  user_id      uuid not null references auth.users(id) on delete cascade,
  device_label text not null default 'device',
  token_hash   text unique not null,
  last_seen    timestamptz,
  created_at   timestamptz not null default now(),
  revoked_at   timestamptz
);
alter table public.daemons enable row level security;

create index daemons_user_active on public.daemons(user_id) where revoked_at is null;

-- Owner can see and revoke their own devices from the web; inserts + last_seen touches happen
-- server-side with the service role (the device token resolves the row, not a user session).
create policy "daemons: own read"
  on public.daemons for select to authenticated using (user_id = auth.uid());
create policy "daemons: own update"
  on public.daemons for update to authenticated using (user_id = auth.uid());
