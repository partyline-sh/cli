-- T1a: at most ONE stall notice per run, enforced by the database.
--
-- The stall sweep runs on every tick and asks "has this triggered run been going too long without
-- reaching a terminal state?" Without a guard that question is true on EVERY subsequent tick, so a
-- run that hangs for an hour would produce sixty identical "no result yet" messages — the exact
-- channel-destroying noise the notification design exists to avoid.
--
-- A read-then-insert check in the sweep would ALMOST work, but two ticks (or two app instances) can
-- interleave between the read and the write, and the failure mode is duplicate alerts about the
-- thing we were trying to reassure someone about. So the constraint lives here instead: the second
-- insert loses, emitEvent's insert returns no id, and the caller simply doesn't deliver. Impossible
-- by construction rather than by care.
--
-- PARTIAL, on purpose. Only run.stalled is once-per-subject; every other kind legitimately repeats
-- for the same run (a run can complete, be restarted, and complete again). Note the predicate is
-- fine here precisely because nothing ever ON CONFLICT-targets this index — that was the trap in the
-- work_items import upsert, where a partial index made the conflict target unusable and the feature
-- never worked once.
create unique index if not exists events_one_stall_per_run
  on public.events (subject_id)
  where kind = 'run.stalled';

comment on index public.events_one_stall_per_run is
  'T1a: a triggered run gets at most one "no result yet" notice, however many ticks observe it stalled.';
