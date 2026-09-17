-- COMMON GROUND slice 6 — projects (the durable substrate) + graduation (thread feed → project
-- canon). A thread is time-boxed and fast-moving; a project is slow-changing team truth
-- (architecture, contracts, conventions). A thread attaches to 0..N projects and inherits their
-- context; a thread block is *graduated* into a project's canon by a human (manual pin, v1),
-- with provenance back to the source block. See docs/COMMON-GROUND.md §3, §10, §12.6.

-- A project: team-scoped durable substrate. Readable by any team member (canon is shared truth).
create table public.projects (
  id         uuid primary key default gen_random_uuid(),
  org_id     uuid not null references public.orgs(id) on delete cascade,
  label      text not null,
  created_by uuid not null references auth.users(id) on delete cascade,
  created_at timestamptz not null default now()
);
alter table public.projects enable row level security;

create policy "projects: read via org"
  on public.projects for select to authenticated
  using (public.is_org_member(org_id));

create index projects_org on public.projects (org_id, created_at desc);

-- A thread attaches to 0..N projects (the seam between two components = one thread on two
-- projects). Readable if you can see the project.
create table public.thread_projects (
  thread_id  uuid not null references public.threads(id) on delete cascade,
  project_id uuid not null references public.projects(id) on delete cascade,
  created_at timestamptz not null default now(),
  primary key (thread_id, project_id)
);
alter table public.thread_projects enable row level security;

create policy "thread_projects: read"
  on public.thread_projects for select to authenticated
  using (exists (select 1 from public.projects p
                 where p.id = project_id and public.is_org_member(p.org_id)));

-- Project canon: the curated, graduated truth. Written ONLY by graduation (service-role) — agents
-- write to threads, never directly to canon (§10). graduated_from is the provenance link to the
-- thread block it came from; supersedes_id links an update to the prior canon it replaced.
create table public.project_blocks (
  id             bigint generated always as identity primary key,
  project_id     uuid not null references public.projects(id) on delete cascade,
  kind           text not null check (kind in ('decision', 'constraint', 'contract', 'question', 'note')),
  body           text not null,
  author         text not null,                                 -- carried from the graduated block
  engine         text,
  status         text not null default 'open' check (status in ('open', 'superseded')),
  graduated_from bigint references public.context_blocks(id) on delete set null,
  supersedes_id  bigint references public.project_blocks(id) on delete set null,
  created_by     uuid not null references auth.users(id) on delete cascade, -- who merged it
  created_at     timestamptz not null default now()
);
alter table public.project_blocks enable row level security;

create policy "project_blocks: read via project"
  on public.project_blocks for select to authenticated
  using (exists (select 1 from public.projects p
                 where p.id = project_id and public.is_org_member(p.org_id)));

create index project_blocks_project on public.project_blocks (project_id, id);
