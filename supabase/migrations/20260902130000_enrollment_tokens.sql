-- ENROLMENT TOKENS — adding the Nth machine without the Nth browser round trip.
--
-- Every machine had to complete the device flow independently: run a command, read a code, open a
-- browser, approve. Correct for the first machine, where a human is proving who they are. Tedious
-- for the fifth, where the same human is proving the same thing again — and impossible on a box
-- with no browser and no second screen, which is most servers.
--
-- So: an operator authenticates ONCE, mints one of these, and pastes it on as many machines as they
-- are setting up. Same shape as a Tailscale auth key, and chosen for the same reason — it is the
-- well-trodden answer to "one person, many machines", not something invented here.
--
-- WHAT IT CAN DO, AND NOTHING ELSE. This token is accepted at exactly one endpoint
-- (/api/v1/daemon/register) and buys exactly one thing: minting a DEVICE token for the user who
-- created it. It is not a session. It cannot read a board, list projects, launch a run, or see
-- anything at all. The blast radius of a leaked enrolment token is "a stranger's machine appears in
-- your fleet, offline until you approve work for it" — visible on the fleet page and revocable —
-- rather than access to an account.
--
-- BOUNDED BY DEFAULT, IN BOTH DIMENSIONS. Time (expires_at, required — there is no non-expiring
-- form) and count (max_uses). A credential that is pasted into terminals and shell histories must
-- stop being useful on its own, without anyone remembering to clean it up.
--
-- ONLY THE HASH IS STORED, exactly as daemons.token_hash and the login tokens do: the raw value is
-- returned once at creation and is unrecoverable afterwards.
create table if not exists public.enrollment_tokens (
  id         uuid primary key default gen_random_uuid(),
  user_id    uuid not null references auth.users(id) on delete cascade,

  -- sha256 of the raw plt_enr_… value. Unique so a collision is a database error, never a
  -- silent second owner for one secret.
  token_hash text unique not null,

  -- What the operator called it ("laptops", "build boxes") — cosmetic, for the revoke list.
  label      text not null default 'enrolment token',

  -- REQUIRED. Enforced by the column being not null rather than by a default the API could
  -- forget to set: there must be no code path that mints one of these without an end date.
  expires_at timestamptz not null,

  -- 0 means unlimited within the lifetime; the API's default is a small number.
  max_uses   integer not null default 10 check (max_uses >= 0),
  uses       integer not null default 0 check (uses >= 0),

  revoked_at timestamptz,
  last_used_at timestamptz,
  created_at timestamptz not null default now()
);

alter table public.enrollment_tokens enable row level security;

-- Lookup is by hash on a hot path (every enrolment), and only live rows are ever candidates.
create index if not exists enrollment_tokens_live on public.enrollment_tokens(token_hash) where revoked_at is null;
create index if not exists enrollment_tokens_owner on public.enrollment_tokens(user_id) where revoked_at is null;

-- OWNER READS AND REVOKES; NOBODY INSERTS THROUGH RLS.
--
-- Minting and redeeming both happen server-side through the service role: creation needs to
-- compute a hash the client must never choose, and redemption happens on a request that has no
-- user session by definition — the whole point is that the enrolling machine is not signed in.
--
-- Note the read policy does NOT expose token_hash to any client; the API selects columns
-- explicitly. Even if it did, a hash is not a credential.
-- Dropped first so re-running this file against a live database is a no-op rather than an error,
-- the same shape the instance_settings migration uses. A migration that cannot be re-applied is a
-- migration that fails a restore-and-replay.
drop policy if exists "enrollment_tokens: own read" on public.enrollment_tokens;
create policy "enrollment_tokens: own read"
  on public.enrollment_tokens for select to authenticated using (user_id = auth.uid());

drop policy if exists "enrollment_tokens: own revoke" on public.enrollment_tokens;
create policy "enrollment_tokens: own revoke"
  on public.enrollment_tokens for update to authenticated using (user_id = auth.uid());

comment on table public.enrollment_tokens is
  'Short-lived, count-bounded credentials that authorise ONE action: minting a device token for the owning user at /api/v1/daemon/register. Not a session; grants no read access.';
