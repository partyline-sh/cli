-- Edge E2 phase 1 (#750): the event substrate. ADDITIVE — nothing consumes this yet and notify()
-- is untouched.
--
-- Why a substrate at all: notify() sends email and Slack when a run finishes. Nothing downstream can
-- react, and it has already failed the way parallel fan-outs fail — the Slack signup alert was
-- chained off the Loops sync's OUTCOME, so one unset marketing key silenced founder alerting
-- entirely, both failures silent. Bolting webhooks on beside notify() would recreate that with a
-- third consumer. So: one producer, many subscribers.
--
-- Phase 1 (this file + emitEvent): events are RECORDED alongside the existing notifications, so the
-- stream can be checked against real traffic before anything depends on it.
-- Phase 2: notify() becomes a subscriber and the inline calls go away.
-- Phase 3 (#747): webhooks become a second subscriber.

create table if not exists public.events (
  id          bigserial primary key,
  org_id      uuid not null references public.orgs(id) on delete cascade,
  -- Dotted, past-tense, and a CLOSED set: an event nobody can subscribe to by name is not an event.
  kind        text not null check (kind in (
                'run.completed','run.failed','run.killed','run.needs_approval','work_item.shipped','trigger.fired'
              )),
  -- What it happened to. Untyped id on purpose — subject_type says how to read it, and a FK per
  -- subject kind would mean one nullable column and one cascade rule per type.
  subject_type text not null check (subject_type in ('run','work_item','trigger')),
  subject_id   uuid not null,
  -- IDS AND LINKS, NEVER PAYLOADS. The whole point: small events, a stable schema, and no customer
  -- content sitting in a third party's logs. A destination that wants detail calls back (E4, #748).
  url          text,
  -- Small, non-content facts a subscriber routes on without a callback: status, preset, project
  -- label. Deliberately NOT task text, diffs, summaries or anything a human wrote.
  meta         jsonb not null default '{}'::jsonb,
  created_at   timestamptz not null default now(),
  -- Set once a subscriber has fanned this out. Null = pending, which is what the ticker drains.
  delivered_at timestamptz
);

-- The ticker's query: undelivered, oldest first. Partial so the index stays the size of the backlog
-- rather than the size of history.
create index if not exists events_pending on public.events (created_at) where delivered_at is null;
create index if not exists events_org_created on public.events (org_id, created_at desc);
create index if not exists events_subject on public.events (subject_type, subject_id);

alter table public.events enable row level security;

-- Read: org members, for the activity/event surfaces. Writes are service-role only — an event is a
-- statement of fact by the system, and a client-authored one is a lie waiting to happen.
create policy "events: org read"
  on public.events for select to authenticated
  using (public.is_org_member(org_id));

comment on table public.events is
  'Edge E2 (#750): one append-only event stream. Ids and links, never payloads. Phase 1 — recorded alongside notify(), not yet consumed.';
