-- Cancel an open consult — the asker withdrawing a question they no longer need answered.
--
-- WHY IT DIDN'T EXIST. Every other transition in this table is driven by the ANSWERING side (the target
-- daemon posts an answer or a decline) or by the clock (the lazy timeout sweep). The asker had no verb
-- at all: `esc` on the wait hands the ask to a local watcher rather than withdrawing it, so a question
-- asked by mistake, or one whose answer stopped mattering, sat `pending` until the 10-minute window
-- closed — and the peer's machine could still burn a read-only engine turn answering it.
--
-- 'canceled' is the A2A TaskState spelling the CLI's local store already uses for this state (see
-- peer_store.go); it had no producer until now precisely because nothing could withdraw an ask.
alter table public.consults drop constraint consults_status_check;
alter table public.consults add constraint consults_status_check
  check (status in ('pending', 'delivered', 'answered', 'declined', 'timed_out', 'failed', 'canceled'));

-- The target daemon has to LEARN the question was withdrawn, or it goes on holding a pending consult
-- (and offering the owner an approve that can only fail). It learns the same way it learns about every
-- other daemon-directed event: off its own SSE stream. This is that push's de-dupe stamp, exactly like
-- answer_routed_at for the answer going the other way — stamped after emit so a reconnect doesn't
-- re-announce a withdrawal the daemon has already acted on.
alter table public.consults add column cancel_routed_at timestamptz;

-- The per-daemon routing selector for the cancel push (mirrors consults_target_pending).
create index consults_target_canceled on public.consults (target_daemon)
  where status = 'canceled' and cancel_routed_at is null;
