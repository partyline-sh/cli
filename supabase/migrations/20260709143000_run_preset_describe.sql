-- Slice 3b: one-shot async web describe reuses the runs table with a new preset, 'describe'. A describe
-- run carries the user's idea as its single task; the daemon (preset=='describe') does NOT spawn crank —
-- it turns the idea into a scored work item via a local claude turn and files it in the planning tree.
-- Widen the runs.preset check to admit 'describe' so createQueuedRun can insert it.

alter table public.runs drop constraint if exists runs_preset_check;
alter table public.runs add constraint runs_preset_check
  check (preset in ('spec', 'chat', 'build', 'describe'));
