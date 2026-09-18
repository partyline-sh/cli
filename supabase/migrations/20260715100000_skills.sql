-- ORG-LEVEL SKILL LIBRARY (v1) — reusable procedural instructions in the Agent Skills open-standard
-- format (a SKILL.md: name + description + markdown body). Org members push skills; the Go daemon
-- fetches the org's ENABLED skills at run launch and writes each into the worker's workspace as
-- .agents/skills/<name>/SKILL.md, injecting them into every agent run. Applied manually to prod.
--
-- Two tables: `skills` is the head (one row per org+name, the enable switch + current description);
-- `skill_versions` is the append-only body history (MAX(version) per skill = the live body). A push
-- is BOTH "create" and "publish a new version" — anyone in the org can push, live immediately (the
-- decided trust model). Delete is owner/admin only and cascades the versions.
--
-- `name` is a PATH-INJECTION BOUNDARY: it becomes .agents/skills/<name>/ on every daemon, so it is
-- pinned to a strict slug (lowercase alnum + dashes, ≤39 chars) in a CHECK here AND re-validated in
-- the API. `body` is bounded so a runaway push can't blow up the injected workspace.

create table public.skills (
  id          uuid primary key default gen_random_uuid(),
  org_id      uuid not null references public.orgs(id) on delete cascade,
  name        text not null check (name ~ '^[a-z0-9][a-z0-9-]{0,38}$'),  -- path-safe slug (see header)
  description text not null default '',
  enabled     boolean not null default true,
  created_by  uuid references auth.users(id) on delete set null,
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now(),
  unique (org_id, name)
);
alter table public.skills enable row level security;

-- Read/push are open to any org MEMBER (the shared library is team truth, and anyone can push — the
-- trust model). Delete is role-gated to owners/admins (mirrors the parties UPDATE / org_members
-- posture). No service-role dependency for the authenticated CRUD — the /api/v1/skills routes run
-- through the caller's RLS-scoped client, so RLS is the real boundary.
create policy "skills: members read"
  on public.skills for select to authenticated
  using (public.is_org_member(org_id));
create policy "skills: members insert"
  on public.skills for insert to authenticated
  with check (public.is_org_member(org_id) and created_by = auth.uid());
create policy "skills: members update"
  on public.skills for update to authenticated
  using (public.is_org_member(org_id))
  with check (public.is_org_member(org_id));
create policy "skills: owners/admins delete"
  on public.skills for delete to authenticated
  using (public.org_role(org_id) in ('owner', 'admin'));

create index skills_org on public.skills (org_id, name);

-- Append-only version history. The row with MAX(version) per skill is the current body; older rows
-- are the audit trail shown as "version history". unique(skill_id, version) makes the "next version =
-- prev max + 1" push atomic — a racing double-push collides on the constraint instead of forking.
create table public.skill_versions (
  id        uuid primary key default gen_random_uuid(),
  skill_id  uuid not null references public.skills(id) on delete cascade,
  body      text not null check (char_length(body) <= 100000),  -- bounded: injected into the workspace
  version   int not null,
  pushed_by uuid references auth.users(id) on delete set null,
  pushed_at timestamptz not null default now(),
  unique (skill_id, version)
);
alter table public.skill_versions enable row level security;

-- Read/insert authorized through the PARENT skill's org membership (join through skill_id). History is
-- immutable — no UPDATE/DELETE policy (a skill delete cascades these via the FK, which bypasses RLS).
create policy "skill_versions: members read via skill"
  on public.skill_versions for select to authenticated
  using (exists (select 1 from public.skills s
                 where s.id = skill_versions.skill_id and public.is_org_member(s.org_id)));
create policy "skill_versions: members insert via skill"
  on public.skill_versions for insert to authenticated
  with check (exists (select 1 from public.skills s
                      where s.id = skill_versions.skill_id and public.is_org_member(s.org_id))
              and pushed_by = auth.uid());

create index skill_versions_skill on public.skill_versions (skill_id, version desc);
