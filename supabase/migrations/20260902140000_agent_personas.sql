-- EDITABLE AGENT PERSONAS — the prompts that decide how planning, coding and review agents behave.
--
-- The seam already existed: GET /api/v1/personas/[mode] serves the text and the CLI fetches it, so
-- a persona change needed no CLI release. What it did NOT allow was changing one without a code
-- change and a deploy, because the source was a TypeScript constant. For a product whose thesis is
-- that better planning produces better software, the text of the planning agent is the highest
-- leverage thing in the system and it was the hardest thing to touch.
--
-- VERSIONED, NOT JUST EDITABLE, and that distinction is the point. partyline sells review gates and
-- an auditable trail. A prompt that could be silently overwritten would make "why did last
-- Tuesday's run do that" unanswerable, put no gate at all on the most consequential text here, and
-- let one bad edit quietly degrade every future run. So every save is a NEW ROW and activation is a
-- pointer move: you can read the history, diff it, and roll back.
--
-- Same shape as the skill library (versions, an active pointer, telemetry), deliberately — that is
-- a pattern this codebase already has and its operators already understand.

-- A persona is identified by its MODE KEY (plan, describe, fix, project_setup…), matching
-- PARTY_MODES. The key is the identity; a row exists only once someone has edited that mode.
create table if not exists public.agent_personas (
  key        text primary key,

  -- Display fields. Null means "keep whatever the shipped default says" — an operator editing the
  -- text of the planning agent should not be forced to re-type its name and description.
  name        text,
  description text,

  -- Which version is live. Nullable so a row can exist mid-edit with nothing published; the read
  -- path treats a null pointer exactly like no row at all and serves the shipped default.
  active_version integer,

  updated_at timestamptz not null default now(),
  created_at timestamptz not null default now()
);

-- One row per save. Never updated, never deleted — that is what makes the history trustworthy.
create table if not exists public.agent_persona_versions (
  id         uuid primary key default gen_random_uuid(),
  key        text not null references public.agent_personas(key) on delete cascade,

  -- Monotonic per persona, assigned server-side. Unique with `key` so two concurrent saves cannot
  -- both claim the same version number — the second gets a constraint violation and retries,
  -- rather than one edit silently overwriting the other.
  version    integer not null,

  -- The actual prompt text.
  preamble   text not null,

  -- Why this edit was made. Optional, and the thing that makes the history readable a month later.
  note       text,

  -- Who saved it. Kept even if the account is later deleted: an audit trail that erases its
  -- authorship is not an audit trail, so this deliberately does NOT cascade.
  author_id  uuid references auth.users(id) on delete set null,

  created_at timestamptz not null default now(),
  unique (key, version)
);

create index if not exists agent_persona_versions_key on public.agent_persona_versions(key, version desc);

alter table public.agent_personas enable row level security;
alter table public.agent_persona_versions enable row level security;

-- READ BY ANY SIGNED-IN USER, WRITTEN BY NOBODY THROUGH RLS.
--
-- Reads are open because every agent on every machine needs the text, and it is product copy rather
-- than a secret. Writes go through the service role behind an instance-admin check in the route —
-- the same posture as instance_settings, and for the same reason: changing how every agent in the
-- deployment behaves is an operator action, not a team-member one.
drop policy if exists "agent_personas: readable by signed-in users" on public.agent_personas;
create policy "agent_personas: readable by signed-in users"
  on public.agent_personas for select using (auth.uid() is not null);

drop policy if exists "agent_persona_versions: readable by signed-in users" on public.agent_persona_versions;
create policy "agent_persona_versions: readable by signed-in users"
  on public.agent_persona_versions for select using (auth.uid() is not null);

-- NO SEED ROWS, ON PURPOSE. The shipped PARTY_MODES text stays the default and the read path falls
-- back to it, so an untouched deployment behaves exactly as before and keeps receiving improvements
-- to the defaults on upgrade. A row appears the first time someone edits that persona — at which
-- point they have taken ownership of it, and pinning it is what they asked for.
comment on table public.agent_personas is
  'Operator overrides for shipped agent personas. Absent key = the built-in default, which continues to track releases.';
