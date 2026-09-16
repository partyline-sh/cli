-- INSTANCE SETTINGS — what this deployment is, as opposed to what a team or a project is.
--
-- partyline is becoming self-host-only, which introduces a scope it has never had. `orgs` answers
-- "what does this team want", `projects` answers "what does this repo need". Neither can answer
-- "has this box been set up yet", because on partyline.sh the answer was always yes: the operator
-- configured it once, by hand, before anyone signed up. A stranger running the container has no
-- such person.
--
-- SINGLETON, ENFORCED BY THE DATABASE. One row, id fixed to true by a check constraint, so there is
-- no such thing as "which settings row is the real one" — a question that has no good answer at
-- 3am. Insert-on-read is deliberate: reading settings on a fresh database creates the defaults
-- rather than returning null and making every caller handle an absent row.
create table if not exists public.instance_settings (
  id boolean primary key default true,
  constraint instance_settings_singleton check (id),

  -- What this instance calls itself. Shown in the shell and in emails; a self-hoster running two
  -- (staging and production, say) needs to tell them apart at a glance.
  instance_name text not null default 'partyline',

  -- SETUP IS RE-RUNNABLE, SO THIS IS A TIMESTAMP AND NOT A LOCK. It records when setup was last
  -- completed, never that it may not happen again — an install that changes (a new identity
  -- provider, a moved hostname) must be able to walk the same steps without a database edit.
  setup_completed_at timestamptz,

  -- Whether a person who is not yet a user may create an account. Off by default: an instance
  -- reachable from the internet with signups open is a stranger's instance, not yours.
  allow_signups boolean not null default false,

  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

-- READ BY ANY SIGNED-IN USER, WRITTEN BY NOBODY THROUGH RLS.
--
-- The wizard, the settings page and the MCP tools all write through the service role behind an
-- admin check in lib/api — the same posture as every other privileged write here. A policy that
-- let an authenticated user UPDATE this table would let any member of any team rename the instance
-- and reopen signups, which is an instance-wide action and not a team-scoped one.
alter table public.instance_settings enable row level security;

drop policy if exists "instance_settings: readable by signed-in users" on public.instance_settings;
create policy "instance_settings: readable by signed-in users"
  on public.instance_settings for select
  using (auth.uid() is not null);

-- The defaults row. Created here rather than lazily so a fresh install has one from the first read,
-- and `on conflict do nothing` so re-running this migration against a live database is a no-op.
insert into public.instance_settings (id) values (true) on conflict (id) do nothing;
