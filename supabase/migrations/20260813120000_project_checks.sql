-- Epic G.4 — per-project check POLICY. Policy only; never a command.
--
-- THE LINE THIS MIGRATION MUST NOT CROSS, stated here because a future column is where it would
-- get crossed by accident: there is no `command` column and there must never be one. A command
-- supplied by the control plane and executed by a daemon is remote code execution on every machine
-- in the fleet — the same boundary already drawn for visual verify (#143) and for daemon updates
-- (#122). The repo's `.partyline/verify` is authoritative about what a check IS; this table only
-- says whether it is on, whether it blocks, and which paths it cares about.
--
-- That split is what makes the settings page safe: an org admin toggling "lint is advisory" is
-- changing a boolean. If they could type a command, any org member who can reach the API would
-- have a shell on the owner's machine — which is the composition hazard already recorded as an
-- open question about Auto-policy projects.
--
-- WHY IT IS WORTH HAVING AT ALL. Today every line in .partyline/verify is blocking and always-run,
-- so a check that cannot pass yet cannot be listed: partyline's own `npm run lint` has 38
-- pre-existing errors, and adding it would reject every clean diff forever. Advisory severity lets
-- a project watch a check without gating on it, and a path glob stops a Go-only change paying for
-- a full `next build`.

create table if not exists public.project_checks (
  id         uuid primary key default gen_random_uuid(),
  project_id uuid not null references public.projects(id) on delete cascade,
  -- Matches the repo's check name. A row naming a check the repo does not declare is IGNORED by
  -- the daemon rather than an error: settings can legitimately outlive a repo edit, and a stale
  -- row must never conjure a check into existence.
  name       text not null,
  enabled    boolean not null default true,
  -- false = advisory: the check RUNS and its result is recorded, but a failure never quarantines.
  blocking   boolean not null default true,
  -- Empty/null = always run. `web/**` limits the check to tasks that touched that subtree.
  path_glob  text,
  ord        int not null default 0,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (project_id, name)
);

-- Bound the name to the same shape the CLI parser accepts, so a row that could never match a real
-- check cannot be written in the first place.
alter table public.project_checks drop constraint if exists project_checks_name_shape;
alter table public.project_checks add constraint project_checks_name_shape
  check (name ~ '^[a-z][a-z0-9_-]{0,31}$');

-- A glob is DATA the daemon matches paths against — never executed — but bound it anyway so a
-- pathological pattern cannot be stored.
alter table public.project_checks drop constraint if exists project_checks_glob_len;
alter table public.project_checks add constraint project_checks_glob_len
  check (path_glob is null or length(path_glob) <= 200);

alter table public.project_checks enable row level security;

-- Read: any member of the project's org. Same wall as the project itself, joined through it.
drop policy if exists "project_checks: readable by org members" on public.project_checks;
create policy "project_checks: readable by org members" on public.project_checks
  for select using (
    exists (
      select 1 from public.projects p
      where p.id = project_checks.project_id
        and public.is_org_member(p.org_id)
    )
  );

-- Write: org ADMINS only. Turning a blocking check advisory weakens the gate for everyone's work,
-- so it is an administrative act rather than an ordinary member one.
drop policy if exists "project_checks: admins write" on public.project_checks;
create policy "project_checks: admins write" on public.project_checks
  for all using (
    exists (
      select 1 from public.projects p
      join public.org_members m on m.org_id = p.org_id
      where p.id = project_checks.project_id
        and m.user_id = auth.uid()
        and m.role in ('owner', 'admin')
    )
  );

create index if not exists project_checks_project_idx on public.project_checks (project_id, ord);

comment on table public.project_checks is
  'G.4: per-project policy for the checks a repo declares in .partyline/verify. POLICY ONLY — enabled/blocking/path_glob. There is deliberately no command column: a server-supplied command executed by a daemon would be RCE on every machine in the fleet.';
