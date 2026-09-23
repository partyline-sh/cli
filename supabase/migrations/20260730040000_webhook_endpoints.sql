-- Edge E3 (#747): outbound webhooks — a second subscriber to the E2 event stream.
--
-- Runs finish and we send an email or a Slack message. That is a notification, not an event:
-- nothing downstream can react, and partyline cannot be a step in anyone's pipeline. E2 made the
-- facts durable; this delivers them.
--
-- IDS AND LINKS, NEVER PAYLOADS. The event carries what happened and where to look, not the
-- content. Small events, a stable schema, and no customer text sitting in a third party's logs
-- forever. A destination that wants detail calls back with a scoped credential (E1/E4).

create table if not exists public.webhook_endpoints (
  id          uuid primary key default gen_random_uuid(),
  org_id      uuid not null references public.orgs(id) on delete cascade,
  name        text not null,
  url         text not null,
  -- HMAC key for signing. Shown once at creation like any credential; we sign with it, the
  -- receiver verifies with it, and neither side can recover it from us afterwards.
  secret      text not null,
  -- Which kinds this endpoint wants. Empty = all. Subscribing to everything is the lazy default
  -- and it is also how a consumer ends up parsing events it never wanted, so the column exists to
  -- make the choice explicit.
  kinds       text[] not null default '{}',
  enabled     boolean not null default true,
  -- Consecutive failures. Reset on any success. The auto-disable threshold below is the whole
  -- reason this is a counter and not a boolean: a dead endpoint should stop being retried forever,
  -- but one bad afternoon should not kill a working integration.
  fail_count  int not null default 0,
  last_error  text,
  last_sent_at timestamptz,
  disabled_at timestamptz,
  created_by  uuid not null references auth.users(id) on delete cascade,
  created_at  timestamptz not null default now()
);

create index if not exists webhook_endpoints_org on public.webhook_endpoints (org_id) where enabled;

alter table public.webhook_endpoints enable row level security;

-- Read: org members, so a team can see where its events are going. The secret is never selected by
-- any client path (see lib/api/webhooks.ts COLS). Writes are service-role: creating one mints a
-- signing key the caller sees exactly once.
create policy "webhook_endpoints: org read"
  on public.webhook_endpoints for select to authenticated
  using (public.is_org_member(org_id));

-- Per (event, endpoint) delivery attempt. Exists so "did they get it?" has an answer that is not
-- "check their logs" — the question every webhook integration eventually turns into.
create table if not exists public.webhook_deliveries (
  id           bigserial primary key,
  endpoint_id  uuid not null references public.webhook_endpoints(id) on delete cascade,
  event_id     bigint not null references public.events(id) on delete cascade,
  status       int,          -- HTTP status, null = never got a response
  error        text,
  attempts     int not null default 1,
  delivered_at timestamptz,
  created_at   timestamptz not null default now(),
  -- The idempotency guarantee. The ticker is deliberately safe to run twice, from two replicas, at
  -- any frequency — this constraint is what makes that true for webhooks: one event reaches one
  -- endpoint once, no matter how many tickers race.
  unique (endpoint_id, event_id)
);

create index if not exists webhook_deliveries_endpoint on public.webhook_deliveries (endpoint_id, created_at desc);

alter table public.webhook_deliveries enable row level security;

create policy "webhook_deliveries: org read via endpoint"
  on public.webhook_deliveries for select to authenticated
  using (exists (
    select 1 from public.webhook_endpoints e
    where e.id = endpoint_id and public.is_org_member(e.org_id)
  ));

comment on table public.webhook_endpoints is
  'Edge E3 (#747): outbound webhook destinations. Ids and links, never payloads.';
