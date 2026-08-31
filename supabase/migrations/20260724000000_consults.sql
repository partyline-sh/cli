-- ask_peer P0.a — the consult broker table.
--
-- A `consult` is one agent (the asker, via an MCP tool in their session) asking a teammate's
-- agent for read-only feedback on a plan/question, scoped to a specific advertised project.
-- Same reference-not-command invariant as launch_requests: the control plane holds a LABEL +
-- the question TEXT, never a path or command. The target daemon resolves the label against its
-- OWN local registry and answers on its OWN checkout (the answer agent is genuinely read-only —
-- P0.0). Routing is per-daemon: a row names its `target_daemon`, and only that daemon's SSE poll
-- selects it (the per-daemon filter IS the router — no fan-out). The answer routes back to the
-- asker's daemon (`from_daemon`) the same way.
--
-- Trust boundary (authorizeConsult): the asker and the target daemon's owner must share an org
-- AND the target must advertise the label (daemon_projects). Enforced server-side in the route;
-- this table just records the brokered exchange + its state machine.
create table public.consults (
  id            uuid primary key default gen_random_uuid(),
  from_user     uuid references auth.users(id) on delete set null,   -- the asker (user identity)
  from_daemon   uuid references public.daemons(id) on delete set null, -- asker's daemon → routes the answer back over its stream (null = poll-only)
  target_daemon uuid not null references public.daemons(id) on delete cascade, -- the peer being consulted
  project_label text not null,                                        -- the advertised label to answer against (a reference, never a path)
  question      text not null,                                        -- the plan/question text (DATA)
  status        text not null default 'pending'
                check (status in ('pending', 'delivered', 'answered', 'declined', 'timed_out', 'failed')),
  detail        text,                                                 -- decline note / failure reason
  answer        text,                                                 -- the peer agent's read-only answer (DATA)
  delivered_at  timestamptz,                                          -- the target daemon picked it up off its stream
  answered_at   timestamptz,
  answer_routed_at timestamptz,                                       -- the answer was pushed back to the asker's daemon stream (dedupe across reconnects, like launch_requests.ref_used_at)
  created_at    timestamptz not null default now()
);
alter table public.consults enable row level security;

-- Both sides may read the exchange: the asker (audit + poll the handle) and the target daemon's
-- owner (who is answering). Service role writes every transition (the routes + stream).
create policy "consults: asker reads"
  on public.consults for select to authenticated
  using (from_user = auth.uid());

create policy "consults: target owner reads"
  on public.consults for select to authenticated
  using (exists (select 1 from public.daemons d where d.id = target_daemon and d.user_id = auth.uid()));

-- Per-daemon routing selectors (the stream polls these every ~2s).
create index consults_target_pending on public.consults (target_daemon) where status = 'pending';
create index consults_from_answered on public.consults (from_daemon) where status = 'answered' and answer_routed_at is null;
