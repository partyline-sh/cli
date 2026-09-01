-- Importing a backlog from somewhere else: give work_items a VISIBLE, UNSCHEDULED state.
--
-- The gap this closes was found by walking the customer motion by hand. A work item created outside
-- a describe party — by `propose_work_item`, or by any future importer — lands as `draft` with no
-- run and no origin_party_id, and NOTHING renders it:
--
--   • the Build board reads RUNS, and reaches work_items only through run_id
--   • the planning cards key on origin_party_id, so only work born in a party appears
--   • /work/plan was merged away and now redirects
--
-- So the rows exist and no screen can show them. Eleven real items were filed that way before anyone
-- noticed, which is the point: it fails silently, exactly like a webhook that never arrives.
--
-- The other half of the problem is that the only route OUT of draft is `promote`, which calls
-- createQueuedRun — so making an item visible and scheduling it onto a specific machine are the same
-- irreversible act. That is backwards for an import: a team pulling 200 tickets out of Jira wants
-- them visible and orderable FIRST, and scheduled one at a time after triage.
--
-- `planning` is that missing middle: shaped enough to look at, not yet promised to a machine.
--
--   draft      → parked / not ready to show (unchanged)
--   planning   → NEW. on the board, no run, no machine. the import landing state.
--   backlog    → has a queued run (unchanged — promote sets this together with run_id)
--   in_progress / done / archived (unchanged)
--
-- Deliberately NOT a new column on the board: `planning` items sit in the EXISTING backlog column
-- beside queued runs. A separate swimlane would split "work waiting to be built" across two places,
-- and the question a person asks that column is "what is next", not "what has a machine yet".

alter table public.work_items drop constraint if exists work_items_status_check;

alter table public.work_items
  add constraint work_items_status_check
  check (status in ('draft', 'planning', 'backlog', 'in_progress', 'done', 'archived'));

-- The board's query: everything waiting, unscheduled, newest-ranked first. Partial, because
-- `planning` is a small slice against a table dominated by shipped work.
create index if not exists work_items_planning
  on public.work_items (thread_id, rank) where status = 'planning' and run_id is null;

comment on column public.work_items.status is
  'draft = parked · planning = on the board, no run yet (the import landing state) · backlog = has a queued run · in_progress · done · archived';

-- ---------------------------------------------------------------------------
-- Import linkage (was #560). Ships in the same migration as `planning` because
-- neither is useful alone: a landing state with no source link produces unlinked
-- copies, and a source link with nowhere visible to land produces invisible rows.
--
-- THE POSITIONING THIS ENABLES: "how do I import from Jira / Linear / Productboard?
-- Use your LLM." partyline holds no tracker credentials and learns no tracker API.
-- The customer's own LLM already has their tracker connected; it reads the roadmap
-- and calls partyline's import tool with the item plus its URL. Source-agnostic by
-- construction, and it works for trackers we have never heard of.
--
-- That only holds if re-import is SAFE. An LLM will be asked to "import the roadmap"
-- more than once, and without idempotency the second run silently doubles the
-- backlog — which looks like a populated board, not a bug.
alter table public.work_items add column if not exists source_tool text;  -- 'jira' | 'linear' | 'github' | anything
alter table public.work_items add column if not exists source_id   text;  -- the tracker's own id
alter table public.work_items add column if not exists source_url  text;  -- the back-link a human clicks

-- Re-import UPDATES, never duplicates. Partial, so hand-created items (source_tool null)
-- are entirely unconstrained — importing must not change how normal items behave.
create unique index if not exists work_items_source_unique
  on public.work_items (org_id, source_tool, source_id)
  where source_tool is not null and source_id is not null;

comment on column public.work_items.source_url is
  'Back-link to the item in the team''s own tracker. partyline is the execution layer, not a second backlog — this is what keeps an imported item a MIRROR rather than a rival copy.';
