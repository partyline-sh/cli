-- Edge E2 phase 2 (#750): notifications become a SUBSCRIBER of the event stream.
--
-- Phase 1 recorded events alongside the existing notify() calls. This is the claim mark that lets
-- the notification path move onto the stream: one column, set exactly once, by whoever gets there
-- first.
--
-- Why a column and not a deliveries table (the shape webhooks use): webhooks fan out to N endpoints
-- per event, so each pairing needs its own row and its own retry state. Notification is ONE
-- subscriber. A second table would be a join and a lifecycle to keep correct in exchange for
-- nothing.
--
-- The claim is what makes it safe to drive delivery from two places at once — inline at the moment
-- the run transitions (so alerts stay immediate) AND from the 60s ticker (so a crash, a deploy
-- mid-request, or a transient send failure still gets picked up). Both race the same
-- status-guarded update; the loser sends nothing.

alter table public.events add column if not exists notified_at timestamptz;

-- The subscriber's only query: unnotified, oldest first. Partial, because the tail of already-
-- notified events is the overwhelming majority and is never scanned.
create index if not exists events_unnotified
  on public.events (created_at) where notified_at is null;

comment on column public.events.notified_at is
  'Edge E2 (#750): claimed+sent by the notification subscriber. NULL = not yet delivered. Set via a status-guarded update so the inline path and the ticker can never both send.';
