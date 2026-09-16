-- Environment pipeline (epic #683), slice 1: an ORDERED list of environments per project.
--
-- Teams ship through a chain they name themselves — dev/test/UAT/sandbox/preprod/production/main —
-- and each one maps to a branch. Ordered, because "promote to the NEXT environment" is meaningless
-- without a sequence.
--
-- This GENERALISES something that already works rather than inventing it: projects.base_branch is
-- already used (daemon/stream/route.ts sends it and crank "forks from it AND targets it"), so
-- environment at position 0 IS today's base_branch. The API keeps the two in lockstep by writing
-- base_branch from position 0 on save, which means crank needs no change and cannot drift.
--
-- A project with one environment behaves exactly as it does today.

create table if not exists public.project_environments (
  id         uuid primary key default gen_random_uuid(),
  project_id uuid not null references public.projects(id) on delete cascade,
  -- Denormalised from the project so RLS can scope without a join, matching every other table here.
  org_id     uuid not null references public.orgs(id) on delete cascade,
  name       text not null check (length(trim(name)) between 1 and 40),
  branch     text not null check (length(trim(branch)) between 1 and 255),
  -- 0-based. Position 0 is where crank submits; the last position is production (or whatever they
  -- call it). Deferrable so a reorder can rewrite the whole list in one statement without
  -- tripping over itself mid-update.
  position   int  not null check (position >= 0),
  created_at timestamptz not null default now(),
  constraint project_environments_pos_uniq unique (project_id, position) deferrable initially deferred,
  constraint project_environments_name_uniq unique (project_id, name)
);

create index if not exists project_environments_project_idx
  on public.project_environments (project_id, position);

alter table public.project_environments enable row level security;

-- Read mirrors projects ("read via org"). WRITES go through the API with the admin client, the same
-- posture as every other project mutation — there is no authenticated write policy on purpose.
drop policy if exists "project_environments: read via org" on public.project_environments;
create policy "project_environments: read via org"
  on public.project_environments for select to authenticated
  using (public.is_org_member(org_id));

-- Backfill: every project that already has a base_branch gets a one-entry list, so nothing about
-- the existing single-environment behaviour changes. The name is the branch — the human renames it
-- if they want, and adds the rest of their chain.
insert into public.project_environments (project_id, org_id, name, branch, position)
select p.id, p.org_id, p.base_branch, p.base_branch, 0
from public.projects p
where coalesce(trim(p.base_branch), '') <> ''
  and not exists (select 1 from public.project_environments e where e.project_id = p.id);
