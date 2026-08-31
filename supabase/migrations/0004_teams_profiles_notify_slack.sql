-- partyline 0004: teams, rich profiles, notification prefs, slack, planned sessions.
-- Reuses is_org_member/org_role from 0001. APPLIED BY HUMAN (CLAUDE.md rule).

-- ============================================================ profiles (enrich)
alter table public.profiles
  add column if not exists display_name    text,
  add column if not exists avatar_url       text,
  add column if not exists github_username  text,
  add column if not exists timezone         text,
  add column if not exists quiet_start      time,
  add column if not exists quiet_end        time;

-- populate display_name / avatar / github handle from the OAuth metadata Supabase
-- stores on the auth user. (GitHub: user_name + name + avatar_url; Google: name + avatar_url/picture)
create or replace function public.handle_new_user()
returns trigger language plpgsql security definer set search_path = public as $$
declare
  m    jsonb := coalesce(new.raw_user_meta_data, '{}'::jsonb);
  base text := coalesce(nullif(split_part(new.email, '@', 1), ''), 'user');
  uniq text := base || '-' || substr(replace(new.id::text, '-', ''), 1, 6);
begin
  insert into profiles (id, handle, display_name, avatar_url, github_username)
    values (
      new.id, uniq,
      coalesce(m->>'name', m->>'full_name', base),
      coalesce(m->>'avatar_url', m->>'picture'),
      m->>'user_name'
    );
  insert into orgs (name, slug, personal, created_by)
    values (base, uniq, true, new.id); -- org trigger adds owner membership
  return new;
end $$;

-- ============================================================ roles: add 'billing'
alter table public.org_members drop constraint if exists org_members_role_check;
alter table public.org_members
  add constraint org_members_role_check check (role in ('owner','admin','billing','member'));

-- ============================================================ teams
create table public.teams (
  id         uuid primary key default gen_random_uuid(),
  org_id     uuid not null references public.orgs(id) on delete cascade,
  name       text not null,
  slug       text not null,
  created_by uuid not null references auth.users(id),
  created_at timestamptz not null default now(),
  unique (org_id, slug)
);
create table public.team_members (
  team_id    uuid not null references public.teams(id) on delete cascade,
  user_id    uuid not null references auth.users(id) on delete cascade,
  role       text not null default 'member' check (role in ('lead','member')),
  created_at timestamptz not null default now(),
  primary key (team_id, user_id)
);
alter table public.teams enable row level security;
alter table public.team_members enable row level security;

-- security-definer helpers (avoid RLS recursion)
create function public.team_org(team uuid)
returns uuid language sql security definer stable set search_path = public as $$
  select org_id from teams where id = team;
$$;
create function public.is_team_lead(team uuid)
returns boolean language sql security definer stable set search_path = public as $$
  select exists (select 1 from team_members where team_id = team and user_id = auth.uid() and role = 'lead');
$$;

create policy "teams: org members read"
  on public.teams for select to authenticated using (public.is_org_member(org_id));
create policy "teams: org admins write"
  on public.teams for all to authenticated
  using (public.org_role(org_id) in ('owner','admin'))
  with check (public.org_role(org_id) in ('owner','admin'));

create policy "team_members: org members read"
  on public.team_members for select to authenticated
  using (public.is_org_member(public.team_org(team_id)));
create policy "team_members: org admins or team lead manage"
  on public.team_members for all to authenticated
  using (public.org_role(public.team_org(team_id)) in ('owner','admin') or public.is_team_lead(team_id))
  with check (public.org_role(public.team_org(team_id)) in ('owner','admin') or public.is_team_lead(team_id));

-- ============================================================ notification prefs
create table public.notify_prefs (
  user_id    uuid not null references auth.users(id) on delete cascade,
  event_type text not null check (event_type in ('session_invite','team_session','mention','digest')),
  channel    text not null check (channel in ('email','slack')),
  enabled    boolean not null default true,
  primary key (user_id, event_type, channel)
);
alter table public.notify_prefs enable row level security;
create policy "notify_prefs: own"
  on public.notify_prefs for all to authenticated
  using (user_id = auth.uid()) with check (user_id = auth.uid());
-- missing row => default (email on, slack off), resolved in app code.

-- ============================================================ slack
create table public.slack_installs (
  org_id        uuid primary key references public.orgs(id) on delete cascade,
  slack_team_id text not null,
  bot_token     text not null,
  bot_user_id   text,
  installed_by  uuid not null references auth.users(id),
  created_at    timestamptz not null default now()
);
create table public.slack_identities (
  user_id       uuid primary key references auth.users(id) on delete cascade,
  slack_user_id text not null,
  slack_team_id text not null
);
alter table public.slack_installs enable row level security;
alter table public.slack_identities enable row level security;
-- reads only; all writes via service role (which bypasses RLS)
create policy "slack_installs: org admins read"
  on public.slack_installs for select to authenticated
  using (public.org_role(org_id) in ('owner','admin'));
create policy "slack_identities: own read"
  on public.slack_identities for select to authenticated using (user_id = auth.uid());

-- ============================================================ sessions: planned + claim
alter table public.sessions add column if not exists created_by uuid references auth.users(id);
update public.sessions set created_by = host_user where created_by is null;
alter table public.sessions alter column created_by set not null;
alter table public.sessions alter column host_user drop not null;
alter table public.sessions drop constraint if exists sessions_status_check;
alter table public.sessions
  add constraint sessions_status_check check (status in ('planned','live','ended'));

-- creators (not just hosts) can see/insert/update their sessions; host still updates
drop policy if exists "sessions: member hosts insert" on public.sessions;
create policy "sessions: members create"
  on public.sessions for insert to authenticated
  with check (created_by = auth.uid() and public.is_org_member(org_id));
drop policy if exists "sessions: host updates" on public.sessions;
create policy "sessions: creator or host updates"
  on public.sessions for update to authenticated
  using (created_by = auth.uid() or host_user = auth.uid());
-- read policy from 0001 already covers members/invitees/host; add creator:
drop policy if exists "sessions: org members or invitees read" on public.sessions;
create policy "sessions: org members, invitees, host, creator read"
  on public.sessions for select to authenticated
  using ((visibility = 'org' and public.is_org_member(org_id))
         or public.is_session_invitee(id)
         or host_user = auth.uid()
         or created_by = auth.uid());

-- ============================================================ invites: team targets
alter table public.session_invites add column if not exists team_id uuid references public.teams(id) on delete cascade;
-- a member of an invited team can see the session:
create or replace function public.is_session_invitee(sess uuid)
returns boolean language sql security definer stable set search_path = public as $$
  select exists (
    select 1 from session_invites si
    where si.session_id = sess
      and (si.user_id = auth.uid()
           or lower(si.email) = lower(coalesce(auth.jwt()->>'email',''))
           or (si.team_id is not null
               and exists (select 1 from team_members tm
                           where tm.team_id = si.team_id and tm.user_id = auth.uid())))
  );
$$;
