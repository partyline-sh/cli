-- COMMON GROUND — 'overview': a first-class, singular "what this is about" block. It's the frame
-- the atomic decisions/constraints/contracts hang on — rendered + primed FIRST (orientation before
-- the details), one-current (supersede to revise). Seeding drafts it; a human can edit on the web.
-- Same block model as everything else — just a new kind, on both the thread feed and project canon.

alter table public.context_blocks drop constraint if exists context_blocks_kind_check;
alter table public.context_blocks
  add constraint context_blocks_kind_check
  check (kind in ('overview', 'decision', 'constraint', 'contract', 'question', 'note'));

alter table public.project_blocks drop constraint if exists project_blocks_kind_check;
alter table public.project_blocks
  add constraint project_blocks_kind_check
  check (kind in ('overview', 'decision', 'constraint', 'contract', 'question', 'note'));
