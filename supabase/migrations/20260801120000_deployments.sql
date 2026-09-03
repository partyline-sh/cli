-- Deploy monitoring (#818 / #819): a trigger learns whether the thing that called it SUCCEEDED.
--
-- Every deploy provider — GitHub Actions, Vercel, Render, Netlify, Fly, Railway, CircleCI — can do
-- exactly one thing in common: POST when a deploy finishes. Anything that cannot (a shell script, a
-- cron) can run curl. So the transport already exists; what a trigger cannot express is an OUTCOME.
--
-- Deploy monitoring needs two things at once:
--   RECORD every outcome, success and failure alike — the successes are half the metric.
--   ACT on only some of them — nobody wants an agent woken by a green deploy.
--
-- Those pull in opposite directions on today's trigger, which has exactly one behaviour: render a
-- template and start work. Hence both columns below.

-- ── how a trigger learns the outcome ────────────────────────────────────────────────────────────
--
-- Two ways, because providers differ in what they will let you configure, and neither costs us a
-- vendor integration (the settled no-server-side-integrations rule):
--
--   1. THE CALLER STATES IT — `{"outcome": "failed"}`. A GitHub Actions step with `if: failure()`
--      is three lines; an n8n node is a dropdown. Preferred, because it depends on nothing we have
--      to keep in sync with a vendor's payload.
--   2. A DECLARATIVE RULE — for providers that fire one webhook for every result. `outcome_path`
--      names where the status lives, `success_when` names the value that means good.
--
-- Deliberately NOT a third option: classifying the payload with a model. That spends a model call
-- on every green deploy to guess at something the provider already stated plainly.
alter table public.triggers add column if not exists outcome_path text;
alter table public.triggers add column if not exists success_when text;

-- Which outcomes actually start work. NULL = today's behaviour exactly: every call starts a run.
-- That default is load-bearing — every trigger that already exists predates this column, and a
-- migration that silently stopped them firing would be a far worse bug than the one being fixed.
alter table public.triggers add column if not exists act_on text[];

comment on column public.triggers.act_on is
  'Outcomes that start work, e.g. {failed}. NULL = act on every call (pre-deploy-monitoring behaviour).';

-- ── the metric ──────────────────────────────────────────────────────────────────────────────────
--
-- Its own table rather than a typed `events` row. events is an append-only stream of ids and links,
-- read forward by subscribers; this is a set you AGGREGATE over — "failure rate this week",
-- "deploys per day per environment" — and that wants its own columns and its own indexes.
--
-- What it buys, for one webhook: deploy frequency, change failure rate, and mean time to restore.
-- Three of the four DORA metrics.
create table if not exists public.deployments (
  id          uuid primary key default gen_random_uuid(),
  org_id      uuid not null references public.orgs(id) on delete cascade,
  trigger_id  uuid references public.triggers(id) on delete set null,
  -- Free text, from the caller. NOT an enum: the point of this feature is that we never enumerate
  -- vendors, and a check constraint here would make "we support X" a migration instead of a doc.
  provider    text,
  environment text,
  -- Normalised to three words at the application layer so the metric is comparable across
  -- providers that say failure/error/build_failed/cancelled. Anything unrecognised lands as
  -- 'unknown' and is counted separately rather than silently scored as a success.
  outcome     text not null check (outcome in ('succeeded', 'failed', 'unknown')),
  -- The caller's own reference — a git sha, a build id. What a human would search for.
  ref         text,
  url         text,
  duration_ms int,
  -- The investigation this failure woke, when it woke one.
  run_id      uuid references public.runs(id) on delete set null,
  created_at  timestamptz not null default now()
);

-- The shape every dashboard query has: this team, recent first, optionally one environment.
create index if not exists deployments_org_recent
  on public.deployments (org_id, created_at desc);
create index if not exists deployments_env_recent
  on public.deployments (org_id, environment, created_at desc);

alter table public.deployments enable row level security;

-- Read: team members. Writes are service-role only — a deployment row is a FACT the endpoint
-- recorded, not something a member should be able to author or amend. A metric anyone can edit is
-- not a metric.
create policy "deployments: team read"
  on public.deployments for select to authenticated
  using (public.is_org_member(org_id));

comment on table public.deployments is
  'Deploy monitoring (#819): one row per deploy outcome reported through a trigger. Powers deploy frequency, change failure rate and MTTR.';
