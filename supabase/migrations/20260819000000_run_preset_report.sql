-- The `report` preset — an agent that LOOKS and TELLS YOU, and structurally cannot do anything else.
--
-- WHY THIS EXISTS. A trigger fires and wakes an agent to answer "what just happened". That is not a
-- build, but until now the only postures available to a trigger were build postures: the triggers
-- table defaults to `spec`, and createQueuedRun treats spec as isBuild, so every fire got a worktree,
-- a commit and a branch. Eight blocked production deploys on 2026-08-18 produced eight branches whose
-- entire content was a markdown triage note — correct analysis, delivered as repo litter.
--
-- The triggers migration (20260730050000) already named this gap and left it open:
--
--     'spec', not 'triage'. #661 named a triage preset that does not exist … Adding a real preset
--     needs both the code allowlist and the runs_preset_check constraint, which is its own change.
--     'spec' is also the right posture for inbound: it produces a proposal, not a build.
--
-- That last sentence was the mistaken part, and it is what this migration corrects: `spec` IS a build
-- posture. This is that change.

alter table public.runs drop constraint if exists runs_preset_check;
alter table public.runs add constraint runs_preset_check
  check (preset in ('spec', 'chat', 'build', 'describe', 'review', 'rebase', 'report'));

-- The finding, in a shape something can DECIDE on.
--
-- The evaluation itself is prose and lives where every other run's narrative already lives
-- (run_tasks.summary, rendered as markdown on the run page). What prose cannot do is be filtered, so
-- the verdict is a column: it is what decides whether a human is interrupted.
--
-- Two values, not five, because it drives exactly one binary decision — notify or stay quiet. A
-- severity ladder would invite a middle rung that nobody can define and everybody argues about.
--   ok        — looked, nothing needs a human. Recorded, silent.
--   attention — a human needs to see this. Recorded AND notified.
-- NULL means no verdict was reported (an older daemon, or a run that never got that far), which is
-- deliberately distinct from `ok`: "nobody said" must never read as "all clear".
alter table public.runs add column if not exists verdict text
  check (verdict is null or verdict in ('ok', 'attention'));

-- The headline — one line, the thing a notification subject is made of. "the deploy guard fired
-- correctly, nothing shipped" is the whole message for most reports; the body is for when you want
-- the reasoning.
alter table public.runs add column if not exists verdict_reason text;

-- New triggers default to reporting rather than building.
--
-- This changes the default for rows created FROM NOW ON. Existing triggers keep whatever they were
-- given, on purpose: a trigger's preset is the owner's configuration, and silently rewriting it
-- would be partyline changing someone's automation behind their back — the exact class of surprise
-- this whole change is about. A deploy-triage trigger still on 'spec' keeps producing branches until
-- its owner moves it, which they can do with:
--
--     update public.triggers set preset = 'report' where id = '<trigger-id>';
--
-- or from the trigger's settings. The safe posture is the DEFAULT; it is not retroactive.
alter table public.triggers alter column preset set default 'report';
