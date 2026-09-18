-- Party activity: widen the `stream` vocabulary so LIVENESS is a reported FACT, not a browser guess.
--
-- Until now party_activity carried only step output ('step'/'stdout'/'stderr'), and the web INFERRED
-- whether an agent was working from it: addressed-less-than-90s-ago, extended by a step row in the last
-- 45s. A long single generation emits no step rows, so at 90 seconds the "is working" strip vanished
-- and the room read as idle while the agent was still composing. The runner now ASSERTS liveness on
-- this same channel — a heartbeat at turn start, every ~10s while generating, and at turn end.
--
-- Two new stream kinds, both METADATA for the indicator rather than lines a human is meant to read:
--   'heartbeat' — body is the turn phase: 'start' | 'alive' | 'end'
--   'usage'     — body is {"in":N,"out":N}, the running token tally
--
-- 'usage' is not new to the CLI: party_agent.go has emitted it since the working-wave token counter
-- landed, but this constraint rejected the value, so …/activity's whitelist coerced it to 'step' and
-- the raw JSON rendered in the feed as though it were a step. Naming it here fixes that too.
--
-- ADDITIVE and backward-compatible: widening a check constraint accepts every row the old app writes,
-- and the old app reads unknown streams as ordinary rows.
--
-- Ordering: this SHOULD land before the API route starts forwarding the new stream values, per the
-- repo's migration rule. It is no longer load-bearing, because the deploy window makes the ordering
-- impossible to hold perfectly anyway (the web image swaps either side of the migration step, and a
-- rollback puts the old schema back under the new code). …/parties/[id]/activity therefore retries a
-- rejected batch with the metadata rows dropped, so the worst case is heartbeats missing — which is
-- exactly the un-upgraded-daemon fallback — rather than a turn's step output vanishing with them.

alter table public.party_activity drop constraint if exists party_activity_stream_check;
alter table public.party_activity add constraint party_activity_stream_check
  check (stream in ('stdout', 'stderr', 'step', 'usage', 'heartbeat'));

-- Liveness is read by recency per agent, not by seq: the web asks "what is the newest heartbeat from
-- this agent", independent of where the step feed's paging window happens to sit.
create index if not exists party_activity_heartbeat
  on public.party_activity (party_id, created_at desc)
  where stream = 'heartbeat';
