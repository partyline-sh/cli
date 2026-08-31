-- Importing a ticket starts a PLANNING CONVERSATION, not a task.
--
-- The first cut of import landed tickets as work_items in a new `planning` status. That was wrong in
-- two ways, and the second is the one that matters.
--
-- Mechanically: nothing rendered them. Work items reach the board only through a run, and the
-- planning cards key on origin_party_id, so an imported item was invisible and — once forced onto
-- the board — unclickable, because its id was a work item's and every board action posts to a run.
--
-- Conceptually, and worse: filing a raw ticket as a TASK asserts it is buildable. It is not. A Jira
-- ticket is a statement of a problem written by someone who was not thinking about how it would be
-- built. Handing that to an agent produces a confident, wrong diff, and the readiness gate then has
-- to catch a lie we told ourselves at import time.
--
-- What an unshaped ticket actually is, is a CONVERSATION SOMEONE NEEDS TO HAVE. partyline already
-- has that: a `describe` party, which interviews a rough idea into a plan of runnable tasks with
-- acceptance criteria, and which ALREADY renders in the Planning column with a working Resume.
--
-- So import now seeds one of those with the ticket, and the human works it through the normal
-- planning flow. No new surface, no new state, and the shaping step becomes impossible to skip
-- rather than merely discouraged.
--
-- These columns are what make a re-import safe: the same ticket must resume the SAME conversation,
-- never start a second one. An LLM asked to "import the roadmap" will be asked again next month, and
-- twenty duplicate planning sessions is worse than none.
alter table public.parties add column if not exists source_tool text;  -- 'jira' | 'linear' | 'github' | anything
alter table public.parties add column if not exists source_id   text;  -- the tracker's own id
alter table public.parties add column if not exists source_url  text;  -- back-link a human clicks

-- NOT partial, deliberately. The work_items equivalent was written `where source_tool is not null`
-- and ON CONFLICT cannot target a partial index — so the upsert failed on every single call and the
-- feature never worked once. Postgres already treats NULLs as distinct here, so ordinary parties
-- (no source) coexist without a predicate.
create unique index if not exists parties_source_unique
  on public.parties (org_id, source_tool, source_id);

comment on column public.parties.source_url is
  'Set when this planning session was started from a ticket in the team''s own tracker. partyline is the execution layer, not a second backlog — this is the back-link that keeps the conversation tied to the original.';
