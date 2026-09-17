-- Auto-curation for Context Threads (#78/#79): SYNTHESIZE + PRUNE, not append.
--
-- A thread accumulates atomic facts forever. That was the right shape for capture — record a
-- decision the moment it happens — but it makes the thread worse as it grows: fifty true, narrow
-- facts are harder to launch a cold worker with than one coherent brief. #79 states the rule:
-- curation must periodically REGENERATE a brief from the facts and RETIRE the ones it absorbed,
-- or the thread becomes a sprawling low-signal dump.
--
-- Everything needed for the review half already exists: `proposed` (0029) and `pruned` (0030),
-- plus supersedes_id from 0026. The one missing piece is the LINK between a synthesized block and
-- the facts it stands in for — so that accepting the synthesis is what retires them, atomically,
-- rather than a human hand-pruning fifty rows afterwards (which nobody will do).
--
-- absorbs holds those ids while the block is `proposed`. Accepting supersedes each one, pointing
-- its supersedes_id at the new block; rejecting touches nothing. Rows are never deleted — a
-- superseded fact stays readable as history, which is what makes the retirement safe to automate.
alter table public.context_blocks
  add column if not exists absorbs bigint[];

comment on column public.context_blocks.absorbs is
  'Curation only: ids of the facts this synthesized block stands in for. Applied on accept — each '
  'is set status=superseded with supersedes_id pointing here. Null for ordinary blocks.';

-- The curated brief is a first-class kind, not a note. It is what the launch primer leads with and
-- what a cold crank worker reads before anything else, so it must be distinguishable from both the
-- hand-written `overview` (a human's 2-4 sentence framing) and the atomic facts it summarises.
--
-- Kept SEPARATE from overview deliberately: a human's framing of what a project IS should not be
-- silently replaced by a machine's summary of what has been decided. They answer different
-- questions and a reader wants both.
alter table public.context_blocks
  drop constraint if exists context_blocks_kind_check;
alter table public.context_blocks
  add constraint context_blocks_kind_check
  check (kind in ('decision', 'constraint', 'contract', 'question', 'note', 'overview', 'brief'));
