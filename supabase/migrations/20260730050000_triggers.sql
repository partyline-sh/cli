-- Edge E5 (#751): triggers — webhooks IN. Other software starts work here.
--
-- The invariant this table exists to enforce: the CALLER SENDS DATA, NEVER WHAT TO RUN.
--
-- A trigger row holds the project, preset, engine, model and merge policy — chosen once by a human
-- who has the authority to choose them. The inbound request carries only facts about what happened
-- (a title, a link, a reference). It cannot name a project it shouldn't touch, cannot pick a
-- write-capable preset, cannot select `auto` merge. Same reference-not-command line the daemon
-- already holds: the outside world says WHAT HAPPENED, partyline decides WHAT RUNS.
--
-- Without that split, "webhooks in" means an HTTP endpoint that runs arbitrary agent work in a
-- chosen repo — which is a remote code execution feature with extra steps.

create table if not exists public.triggers (
  id            uuid primary key default gen_random_uuid(),
  org_id        uuid not null references public.orgs(id) on delete cascade,
  name          text not null,
  -- The URL path: POST /api/v1/t/<slug>. Unique per org so a slug is guessable but not
  -- cross-tenant; the credential is what authorizes, not the slug.
  slug          text not null,
  project_id    uuid not null references public.projects(id) on delete cascade,
  -- WHICH MACHINE runs it. The spec left this open ("pin a daemon, or let the fleet choose?") and
  -- the answer is forced rather than chosen: createQueuedRun requires a daemon, and no
  -- fleet-selection mechanism exists yet. So a trigger pins one.
  --
  -- The cost is real and worth stating: if that machine is offline, the trigger queues against a
  -- dead node and production alerts pile up silently. Letting the dispatcher choose an online node
  -- is the better end state and is follow-up work, not something to fake here.
  daemon_id     uuid not null references public.daemons(id) on delete cascade,
  -- 'spec', not 'triage'. #661 named a triage preset that does not exist: createQueuedRun's
  -- allowlist is spec|chat|build|describe|review|rebase, so 'triage' would have been SILENTLY
  -- coerced to spec on every fire — a default that quietly becomes something else. Adding a real
  -- preset needs both the code allowlist and the runs_preset_check constraint, which is its own
  -- change. 'spec' is also the right posture for inbound: it produces a proposal, not a build.
  preset        text not null default 'spec',
  engine        text,
  model         text,
  merge_policy  text not null default 'manual',
  -- The template rendered with the caller's data. This is the only place inbound text ends up, and
  -- it is rendered as fenced, labelled DATA — a Sentry title is a string a stranger can write.
  task_template text not null,
  -- review = lands in the backlog for a human to start (default).
  -- auto    = dispatches immediately. An explicit admin opt-in, exactly as merge_policy 'auto'
  --           required its branch-protection gate. Auto-dispatch PLUS a write-capable preset is
  --           the composition to watch, and the reason these are two separate columns.
  gate          text not null default 'review' check (gate in ('review', 'auto')),
  enabled       boolean not null default true,
  last_fired_at timestamptz,
  fire_count    int not null default 0,
  created_by    uuid not null references auth.users(id) on delete cascade,
  created_at    timestamptz not null default now(),
  unique (org_id, slug)
);

create index if not exists triggers_slug on public.triggers (slug) where enabled;

alter table public.triggers enable row level security;

create policy "triggers: org read"
  on public.triggers for select to authenticated
  using (public.is_org_member(org_id));

-- Every inbound call, whether or not it started anything. This is the audit trail for "who made my
-- factory build that", and the dedupe key.
create table if not exists public.trigger_fires (
  id         bigserial primary key,
  trigger_id uuid not null references public.triggers(id) on delete cascade,
  -- The caller's own reference for the thing that happened (ticket id, alert id). iPaaS redelivery
  -- is routine, not exceptional, so the same ref must never start two runs.
  ref        text,
  run_id     uuid references public.runs(id) on delete set null,
  -- Why nothing ran, when nothing ran: duplicate, disabled, rejected. A trigger that silently does
  -- nothing is indistinguishable from a broken one.
  skipped    text,
  created_at timestamptz not null default now()
);

-- Partial unique: dedupe only applies when the caller supplied a ref. No ref means they accepted
-- at-least-once, and we do not invent an identity for them.
create unique index if not exists trigger_fires_dedupe
  on public.trigger_fires (trigger_id, ref) where ref is not null;

create index if not exists trigger_fires_recent on public.trigger_fires (trigger_id, created_at desc);

alter table public.trigger_fires enable row level security;

create policy "trigger_fires: org read via trigger"
  on public.trigger_fires for select to authenticated
  using (exists (
    select 1 from public.triggers t
    where t.id = trigger_id and public.is_org_member(t.org_id)
  ));

comment on table public.triggers is
  'Edge E5 (#751): saved inbound entry points. The caller sends data; the row decides what runs.';
