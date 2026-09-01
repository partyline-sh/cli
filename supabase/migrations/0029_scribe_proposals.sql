-- COMMON GROUND slice 7a — the ambient scribe (server-side party-channel distiller) proposes
-- facts; a human confirms. Safety by construction: a proposed block is HIDDEN from agents
-- (recall/primer) until someone accepts it, so a mediocre distill pass can never poison the
-- shared context — the worst case is noise in a review queue. See docs/COMMON-GROUND.md §3/§4.

-- context_blocks gains a 'proposed' status (scribe suggestion, awaiting human accept → 'open').
alter table public.context_blocks drop constraint if exists context_blocks_status_check;
alter table public.context_blocks
  add constraint context_blocks_status_check
  check (status in ('proposed', 'open', 'superseded', 'graduated'));

-- A party can be linked to a Common Ground thread (its distilled facts land there), and carries
-- a watermark of the last channel message the scribe has processed (so it never re-distills).
alter table public.parties add column if not exists thread_id   uuid references public.threads(id) on delete set null;
alter table public.parties add column if not exists scribe_upto bigint not null default 0;
