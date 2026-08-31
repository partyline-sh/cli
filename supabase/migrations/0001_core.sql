-- partyline 0001_core: multi-tenant foundation
-- Tenancy: org = tenant. Every signup gets a personal org. RLS enforces
-- membership at the DB layer — clients (web + CLI via minted JWT) share one
-- enforcement path. Applied BY HUMAN per CLAUDE.md hard rule.

-- ============================================================ profiles
create table public.profiles (
  id         uuid primary key references auth.users(id) on delete cascade,
  handle     text unique not null,
  created_at timestamptz not null default now()
);
alter table public.profiles enable row level security;

create policy "profiles: authenticated read"
  on public.profiles for select to authenticated using (true);
create policy "profiles: own update"
  on public.profiles for update to authenticated using (auth.uid() = id);

-- ============================================================ orgs + members
create table public.orgs (
  id         uuid primary key default gen_random_uuid(),
  name       text not null,
  slug       text unique not null,
  personal   boolean not null default false,
  created_by uuid not null references auth.users(id),
  created_at timestamptz not null default now()
);
create table public.org_members (
  org_id     uuid not null references public.orgs(id) on delete cascade,
  user_id    uuid not null references auth.users(id) on delete cascade,
  role       text not null default 'member' check (role in ('owner','admin','member')),
  created_at timestamptz not null default now(),
  primary key (org_id, user_id)
);
alter table public.orgs enable row level security;
alter table public.org_members enable row level security;

-- security-definer helpers (avoid RLS recursion; single source of truth)
create function public.is_org_member(org uuid)
returns boolean language sql security definer stable set search_path = public as $$
  select exists (select 1 from org_members where org_id = org and user_id = auth.uid());
$$;
create function public.org_role(org uuid)
returns text language sql security definer stable set search_path = public as $$
  select role from org_members where org_id = org and user_id = auth.uid();
$$;

create policy "orgs: members read"
  on public.orgs for select to authenticated using (public.is_org_member(id));
create policy "orgs: creator insert"
  on public.orgs for insert to authenticated with check (created_by = auth.uid());
create policy "orgs: admins update"
  on public.orgs for update to authenticated using (public.org_role(id) in ('owner','admin'));

create policy "org_members: members read"
  on public.org_members for select to authenticated using (public.is_org_member(org_id));
create policy "org_members: admins manage"
  on public.org_members for insert to authenticated
  with check (public.org_role(org_id) in ('owner','admin'));
create policy "org_members: admins remove (not owner) or self-leave"
  on public.org_members for delete to authenticated
  using (user_id = auth.uid()
         or (public.org_role(org_id) in ('owner','admin') and role <> 'owner'));

-- creator becomes owner automatically (solves the bootstrap chicken/egg)
create function public.org_after_insert()
returns trigger language plpgsql security definer set search_path = public as $$
begin
  insert into org_members (org_id, user_id, role) values (new.id, new.created_by, 'owner');
  return new;
end $$;
create trigger org_owner_bootstrap after insert on public.orgs
  for each row execute function public.org_after_insert();

-- new auth user → profile + personal org
create function public.handle_new_user()
returns trigger language plpgsql security definer set search_path = public as $$
declare
  h text := coalesce(nullif(split_part(new.email, '@', 1), ''), 'user');
  uniq text := h || '-' || substr(replace(new.id::text, '-', ''), 1, 6);
begin
  insert into profiles (id, handle) values (new.id, uniq);
  insert into orgs (name, slug, personal, created_by)
    values (h, uniq, true, new.id); -- trigger adds owner membership
  return new;
end $$;
create trigger on_auth_user_created after insert on auth.users
  for each row execute function public.handle_new_user();

-- ============================================================ sessions
create table public.sessions (
  id         uuid primary key default gen_random_uuid(),
  org_id     uuid not null references public.orgs(id) on delete cascade,
  host_user  uuid not null references auth.users(id),
  join_code  text unique not null,
  status     text not null default 'live' check (status in ('live','ended')),
  visibility text not null default 'org'  check (visibility in ('org','invite')),
  endpoints  jsonb not null default '[]',
  started_at timestamptz not null default now(),
  last_seen  timestamptz not null default now(),
  ended_at   timestamptz
);
create index sessions_org_live on public.sessions (org_id, status);
alter table public.sessions enable row level security;

create table public.session_invites (
  id         uuid primary key default gen_random_uuid(),
  session_id uuid not null references public.sessions(id) on delete cascade,
  email      text,
  user_id    uuid references auth.users(id),
  channel    text not null default 'email' check (channel in ('email','slack','webhook')),
  created_at timestamptz not null default now(),
  check (email is not null or user_id is not null)
);
alter table public.session_invites enable row level security;

create function public.is_session_invitee(sess uuid)
returns boolean language sql security definer stable set search_path = public as $$
  select exists (
    select 1 from session_invites si
    where si.session_id = sess
      and (si.user_id = auth.uid()
           or lower(si.email) = lower(coalesce(auth.jwt()->>'email','')))
  );
$$;

create policy "sessions: org members or invitees read"
  on public.sessions for select to authenticated
  using ((visibility = 'org' and public.is_org_member(org_id))
         or public.is_session_invitee(id)
         or host_user = auth.uid());
create policy "sessions: member hosts insert"
  on public.sessions for insert to authenticated
  with check (host_user = auth.uid() and public.is_org_member(org_id));
create policy "sessions: host updates"
  on public.sessions for update to authenticated using (host_user = auth.uid());

create policy "session_invites: host or invitee read"
  on public.session_invites for select to authenticated
  using (user_id = auth.uid()
         or lower(email) = lower(coalesce(auth.jwt()->>'email',''))
         or exists (select 1 from sessions s where s.id = session_id and s.host_user = auth.uid()));
create policy "session_invites: host insert"
  on public.session_invites for insert to authenticated
  with check (exists (select 1 from sessions s where s.id = session_id and s.host_user = auth.uid()));

-- ============================================================ org invites
create table public.org_invites (
  id          uuid primary key default gen_random_uuid(),
  org_id      uuid not null references public.orgs(id) on delete cascade,
  email       text not null,
  role        text not null default 'member' check (role in ('admin','member')),
  token       text unique not null,
  status      text not null default 'pending' check (status in ('pending','accepted','revoked')),
  created_by  uuid not null references auth.users(id),
  created_at  timestamptz not null default now(),
  accepted_by uuid references auth.users(id)
);
alter table public.org_invites enable row level security;

create policy "org_invites: admins manage"
  on public.org_invites for all to authenticated
  using (public.org_role(org_id) in ('owner','admin'))
  with check (public.org_role(org_id) in ('owner','admin'));

-- token-based accept must work for users NOT yet in the org → RPC
create function public.accept_org_invite(invite_token text)
returns uuid language plpgsql security definer set search_path = public as $$
declare inv org_invites;
begin
  select * into inv from org_invites
    where token = invite_token and status = 'pending' for update;
  if not found then raise exception 'invalid or used invite'; end if;
  insert into org_members (org_id, user_id, role)
    values (inv.org_id, auth.uid(), inv.role)
    on conflict do nothing;
  update org_invites set status = 'accepted', accepted_by = auth.uid()
    where id = inv.id;
  return inv.org_id;
end $$;

-- ============================================================ CLI auth
-- api_tokens: only the SHA-256 hash is stored. device_codes: service-role only.
create table public.api_tokens (
  id         uuid primary key default gen_random_uuid(),
  user_id    uuid not null references auth.users(id) on delete cascade,
  token_hash text unique not null,
  name       text not null default 'cli',
  created_at timestamptz not null default now(),
  last_used  timestamptz
);
alter table public.api_tokens enable row level security;
create policy "api_tokens: own read"
  on public.api_tokens for select to authenticated using (user_id = auth.uid());
create policy "api_tokens: own revoke"
  on public.api_tokens for delete to authenticated using (user_id = auth.uid());
-- inserts happen server-side with the service role only

create table public.device_codes (
  device_code text primary key,
  user_code   text unique not null,
  status      text not null default 'pending' check (status in ('pending','approved','expired')),
  user_id     uuid references auth.users(id),
  token_id    uuid references public.api_tokens(id),
  created_at  timestamptz not null default now(),
  expires_at  timestamptz not null default now() + interval '15 minutes'
);
alter table public.device_codes enable row level security;
-- intentionally NO policies: deny-all for clients; service role only.

-- ============================================================ realtime
alter publication supabase_realtime add table public.sessions;
