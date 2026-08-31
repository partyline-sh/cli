-- Track whether a session announced its start to Slack, so the "session closed"
-- summary only fires for sessions whose start was posted to a channel (lifecycle
-- symmetry — no close notices for silent sessions). Set true by the live-start
-- --announce path, the Slack /partyline start command, and on claim (claiming a
-- planned session always announces it live). Read by the end route.
alter table public.sessions
  add column if not exists announced boolean not null default false;
