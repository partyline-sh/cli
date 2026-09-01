-- Epic G.5 — more than one reviewer, and agreement as a ranking signal.
--
-- Every other part of the gate makes review more TRUSTWORTHY. This one makes it CHEAPER, which is
-- the only number the business question turns on: minutes of human attention per merged pull
-- request. One reviewer gives you findings in arbitrary order with no way to tell a real defect
-- from a stylistic opinion. Two INDEPENDENT reviewers give you something one cannot buy at any
-- quality — agreement — and a finding both raised from the same diff is far likelier to be real.
--
-- A lane is (engine, model). Nothing here is a command: the daemon already knows how to spawn each
-- engine, and this only says WHICH to ask. Same boundary as project_checks.
--
-- COST, stated because it is the reason this is opt-in per project rather than a default. Two lanes
-- is 2× reviewer tokens on the same diff. Lanes run concurrently so wall-clock is roughly
-- unchanged, and per-lane spend is recorded on the gate report so the trade is measured rather than
-- assumed. A project with no rows here keeps exactly today's single-reviewer behaviour.

create table if not exists public.project_review_lanes (
  id         uuid primary key default gen_random_uuid(),
  project_id uuid not null references public.projects(id) on delete cascade,
  -- Short identifier for the lane, shown against each finding as its source ("2/2 reviewers").
  lane_id    text not null,
  -- Which engine to ask. Validated against the closed engine set; the daemon re-validates, since a
  -- server value never becomes an argv without being checked on the machine that runs it.
  engine     text not null,
  -- Free-form and engine-defined, exactly like runs.model. Empty = the engine's own default.
  model      text not null default '',
  enabled    boolean not null default true,
  ord        int not null default 0,
  created_at timestamptz not null default now(),
  unique (project_id, lane_id)
);

alter table public.project_review_lanes drop constraint if exists project_review_lanes_lane_shape;
alter table public.project_review_lanes add constraint project_review_lanes_lane_shape
  check (lane_id ~ '^[a-z][a-z0-9_-]{0,31}$');

-- Generated from internal/surface (S.1). A lane naming an engine the fleet cannot spawn would fail
-- at review time on someone else's machine, which is the worst place to find out.
alter table public.project_review_lanes drop constraint if exists project_review_lanes_engine_check;
alter table public.project_review_lanes add constraint project_review_lanes_engine_check
  check (engine in ('claude', 'codex', 'gemini', 'opencode', 'goose', 'antigravity'));

alter table public.project_review_lanes drop constraint if exists project_review_lanes_model_len;
alter table public.project_review_lanes add constraint project_review_lanes_model_len
  check (length(model) <= 60);

alter table public.project_review_lanes enable row level security;

drop policy if exists "review_lanes: readable by org members" on public.project_review_lanes;
create policy "review_lanes: readable by org members" on public.project_review_lanes
  for select using (
    exists (
      select 1 from public.projects p
      where p.id = project_review_lanes.project_id
        and public.is_org_member(p.org_id)
    )
  );

-- Admin-only, same reasoning as project_checks: removing a reviewer weakens the gate for everyone's
-- work, so it is an administrative act.
drop policy if exists "review_lanes: admins write" on public.project_review_lanes;
create policy "review_lanes: admins write" on public.project_review_lanes
  for all using (
    exists (
      select 1 from public.projects p
      join public.org_members m on m.org_id = p.org_id
      where p.id = project_review_lanes.project_id
        and m.user_id = auth.uid()
        and m.role in ('owner', 'admin')
    )
  );

create index if not exists project_review_lanes_project_idx
  on public.project_review_lanes (project_id, ord);

comment on table public.project_review_lanes is
  'G.5: the reviewer lanes a project runs. (engine, model) only — never a command. No rows = today''s single-reviewer behaviour, which is why this is opt-in: a second lane doubles reviewer tokens on the same diff.';
