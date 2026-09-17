-- Environment pipeline (epic #683), slice 2: the DELTA between adjacent environments.
--
-- "What is built but not live yet" is the question the whole epic exists to answer, and it has one
-- honest source: git. The machine that builds the project already has the clone, so the daemon
-- computes each gap with plain git and reports it here. No GitHub API, no GitLab API, no connector —
-- which is the only way this can work for a customer on Bitbucket, or on a self-hosted remote, or on
-- no remote at all.
--
-- One row per GAP, not per environment: a chain of N environments has N-1 gaps, each keyed by the
-- position of the EARLIER environment. That is the shape the UI reads ("staging → production: 11
-- commits") and it makes the unique constraint fall out naturally.
--
-- Everything here is a CACHE of what git said at reported_at. It is derived, never authoritative —
-- if it is stale or missing, the answer is "we do not know yet", never a wrong answer. That is why
-- there is no trigger, no backfill, and no default row: a project with no daemon reporting simply
-- has no gaps recorded, and the UI says so.

create table if not exists public.project_env_gaps (
  id         uuid primary key default gen_random_uuid(),
  project_id uuid not null references public.projects(id) on delete cascade,
  -- Denormalised from the project so RLS can scope without a join, matching project_environments.
  org_id     uuid not null references public.orgs(id) on delete cascade,

  -- Position of the EARLIER environment in the chain — the one work travels FROM. A gap is always
  -- between position N and N+1, so this single number identifies it.
  position   int  not null check (position >= 0),

  -- Names and branches are DENORMALISED copies of what the chain said when the delta was computed.
  -- Deliberate: a gap row must stay readable after the chain is edited, and the copy is what makes
  -- a stale row obviously stale ("staging → production" when the chain now says something else)
  -- rather than silently re-labelled onto the wrong numbers.
  from_name   text not null,
  to_name     text not null,
  from_branch text not null,
  to_branch   text not null,

  -- How many commits are in from_branch but not yet in to_branch. The headline number.
  commit_count int not null default 0 check (commit_count >= 0),
  -- Distinct commit authors in the gap, most-commits-first. ["Darcy Reno", "crank"] — so the UI can
  -- say WHO is waiting, which is the difference between "11 commits" and "11 commits, 3 of them
  -- yours". Names as git reports them; no attempt to resolve them to partyline accounts.
  authors      jsonb not null default '[]'::jsonb,
  -- The commits themselves, capped by the daemon: [{sha, subject, author, at}]. Subjects only —
  -- never a diff, never file contents. Enough to recognise the work, not enough to leak the code.
  commits      jsonb not null default '[]'::jsonb,
  -- Which of partyline's OWN task branches are in this gap: [{branch, run_id}]. Populated because
  -- partyline CREATED those branches, so it can map them back to runs and work items with no
  -- heuristics — the thing a generic git tool cannot do. Foreign commits (a human pushing directly)
  -- are counted in commit_count but appear here only as commits, which is the honest distinction.
  items        jsonb not null default '[]'::jsonb,

  -- Which machine said so, and when. Both are shown: a delta is only as trustworthy as its
  -- freshness, and a gap last reported two days ago must not read like a live number.
  reported_by uuid references public.daemons(id) on delete set null,
  reported_at timestamptz not null default now(),

  constraint project_env_gaps_uniq unique (project_id, position)
);

create index if not exists project_env_gaps_org_idx on public.project_env_gaps (org_id, project_id);

alter table public.project_env_gaps enable row level security;

-- Read mirrors projects ("read via org"). WRITES are the daemon's, through the device-token endpoint
-- with the admin client — there is no authenticated write policy on purpose, so a user session can
-- never fabricate a delta.
drop policy if exists "project_env_gaps: read via org" on public.project_env_gaps;
create policy "project_env_gaps: read via org"
  on public.project_env_gaps for select to authenticated
  using (public.is_org_member(org_id));
