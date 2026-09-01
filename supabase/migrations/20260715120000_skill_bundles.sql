-- PACKAGE SKILLS — a skill version becomes a BUNDLE, not just a markdown blob. Real skills ship as
-- directories/zips: a SKILL.md (the agent contract, with frontmatter) plus scripts/, references/,
-- assets/, a human README. This migration adds the bundle storage + a usage-telemetry table so the
-- library is curatable ("what's actually used?"). Applied manually to prod. Backwards-compatible:
-- existing rows keep working as body-only (bundle_path null) skills.
--
-- SECURITY POSTURE (decided): anyone pushes AND anyone enables — including skills that carry
-- executable scripts. The guardrails are (a) the RUN's engine tool posture (a bundled script only
-- runs if that run has bash enabled — claude allowlist / codex --allow-bash), (b) STRICT zip-slip /
-- zip-bomb / path-traversal guards on every unpack site (import, daemon materialize, CLI install),
-- (c) provenance (pushed_by, immutable version history), and (d) a visible has_scripts badge so
-- enabling a script-bearing skill is an informed choice. There is NO admin gate.

-- 1) skill_versions gains the bundle. `body` STAYS — it's the extracted SKILL.md, and remains the
-- source for the run injection manifest, the web render, and search (so the common prose skill needs
-- no bundle download). `bundle_path` is the object key in the skill-bundles bucket (null = a legacy /
-- prose-only version with no packaged files). `manifest` is the file listing we can show WITHOUT
-- unzipping: [{ "path": "scripts/deploy.sh", "size": 812, "exec": true }, ...]. `has_scripts` is
-- derived at import (any executable file, or files under scripts/, or a known script extension) and
-- drives the UI badge + the honesty copy.
alter table public.skill_versions
  add column if not exists bundle_path  text,
  add column if not exists manifest     jsonb not null default '[]'::jsonb,
  add column if not exists has_scripts  boolean not null default false,
  add column if not exists bundle_bytes bigint not null default 0;

-- 2) Usage telemetry — one row per (run, skill) the daemon injected, flipped to invoked when the run's
-- event stream shows the agent actually activated the skill (best-effort, engine-dependent). Powers
-- "injected into N runs · invoked in M · last used <when>" so dead skills are visible and the library
-- stays curated. org_id is denormalized for a cheap per-org rollup; skill_name too, so a deleted skill
-- still reads sensibly in history.
create table public.skill_usage (
  id          uuid primary key default gen_random_uuid(),
  org_id      uuid not null references public.orgs(id) on delete cascade,
  skill_id    uuid references public.skills(id) on delete set null,  -- null once the skill is deleted
  skill_name  text not null,
  version     int not null default 0,
  run_id      uuid,                              -- the run it was injected into (advisory; no FK — runs may be pruned)
  injected_at timestamptz not null default now(),
  invoked     boolean not null default false,
  invoked_at  timestamptz
);
alter table public.skill_usage enable row level security;

-- Read: any org member (it's team-visible telemetry). Writes come from the daemon via the service role
-- in the /daemon/runs/[id]/skill-usage route (device-token authorized), so there is NO insert/update
-- policy for authenticated users — the service role bypasses RLS, and members are read-only here.
create policy "skill_usage: members read"
  on public.skill_usage for select to authenticated
  using (public.is_org_member(org_id));

create index skill_usage_skill on public.skill_usage (skill_id, injected_at desc);
create index skill_usage_org on public.skill_usage (org_id, injected_at desc);

-- 3) Private bucket for the bundle zips — no public URLs, every read/write mediated by the service role
-- in the skills routes after proving org membership (web) or the device token (daemon). Mirrors the
-- party-attachments bucket (20260709220000). Object key convention: skills/<skill_id>/<version>.zip.
insert into storage.buckets (id, name, public)
values ('skill-bundles', 'skill-bundles', false)
on conflict (id) do nothing;
