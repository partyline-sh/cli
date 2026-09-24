-- Party LIVE ACTIVITY — the streaming step feed for an agent's in-progress turn. Today a party agent
-- runs the engine silently and posts ONE finished message (party_messages), so the web shows a fake
-- "is working ●●●" for the whole turn. This table is the party twin of run_logs (0055): the runner
-- streams the engine's humanized step output here AS IT WORKS, and the party view tails it live over
-- Realtime, then collapses it when the final message lands.
--
-- Same split as runs: party_messages stays the append-only, immutable channel of FINISHED messages
-- (SSE, transcript artifacts, MCP read_transcript all depend on that). party_activity is HIGH-VOLUME,
-- ephemeral, best-effort telemetry — NOT part of the transcript. `agent` is the agent name (matches the
-- 'agent:<name>' sender on party_messages); `seq` is a producer-assigned monotonic ordering hint (one
-- runner process per agent turn), not a security artifact.

create table public.party_activity (
  id         uuid primary key default gen_random_uuid(),
  party_id   uuid not null references public.parties(id) on delete cascade,
  agent      text not null,                          -- the working agent's name ('agent:<name>' sans prefix)
  seq        bigint not null default 0,              -- producer-assigned monotonic ordering hint
  stream     text not null default 'step'
             check (stream in ('stdout', 'stderr', 'step')),
  body       text not null,                          -- one humanized step/prose line (the agent's own DATA)
  created_at timestamptz not null default now()
);
alter table public.party_activity enable row level security;

-- Read: anyone who can read the PARENT party — identical wall to party_messages (0018), joined through
-- party_id. No cross-party leakage: an activity line is only visible to those who can see its party.
create policy "party_activity: read via party"
  on public.party_activity for select to authenticated
  using (exists (select 1 from public.parties p
                 where p.id = party_activity.party_id
                   and (public.is_org_member(p.org_id) or p.created_by = auth.uid())));

-- No authenticated INSERT/UPDATE/DELETE: the runner appends via the service role through
-- …/parties/[id]/activity, authorized by the party-scoped bearer token. Members read only.

create index party_activity_party on public.party_activity (party_id, seq, created_at);

-- REALTIME — the party view subscribes to party_activity INSERTs (mirrors run_logs in run-tasks.tsx).
-- RLS still authorizes each subscriber per row via the read-via-party policy above. Guarded so a re-run
-- or a publication-absent environment is a no-op rather than an error.
do $$
begin
  alter publication supabase_realtime add table public.party_activity;
exception when undefined_object then null; when duplicate_object then null;
end $$;
