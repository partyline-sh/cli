-- COMMON GROUND slice 7 — per-team ambient-scribe config. A team chooses how ambient capture
-- runs: partyline's default model, their OWN model (BYOM: provider + model + key), or off. The
-- BYOM key is stored encrypted (wrapSessionKey / AES-256-GCM, wrap secret held outside Postgres)
-- and NEVER returned to a client — the API projects it to a boolean "key set". See COMMON-GROUND
-- §4/§7 and docs/COMMON-GROUND.md.

create table public.team_scribe (
  org_id     uuid primary key references public.orgs(id) on delete cascade,
  mode       text not null default 'partyline' check (mode in ('partyline', 'byom', 'off')),
  provider   text check (provider in ('anthropic', 'openai', 'xai', 'google')),
  model      text,
  key_cipher text,                                            -- encrypted BYOM key; never sent to clients
  updated_by uuid references auth.users(id) on delete set null,
  updated_at timestamptz not null default now()
);
alter table public.team_scribe enable row level security;

-- Any team member may read the config (to see how capture is set up); the key_cipher is
-- ciphertext (useless without the server-only wrap secret) and the API omits it regardless.
-- Writes are service-role, gated in the route to org owners/admins.
create policy "team_scribe: read via org"
  on public.team_scribe for select to authenticated
  using (public.is_org_member(org_id));
