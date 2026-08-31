-- COMMON GROUND — soft-delete ("prune"). A pruned block is removed from the live shared context
-- (hidden from agents, like 'superseded'/'proposed') but kept in history for the audit trail —
-- we never hard-delete a fact. Prune + Revert are the version-control edit ops in the timeline
-- UI (COMMON-GROUND §9). See also the display vocabulary: supersede→"replace", graduate→"promote".

alter table public.context_blocks drop constraint if exists context_blocks_status_check;
alter table public.context_blocks
  add constraint context_blocks_status_check
  check (status in ('proposed', 'open', 'superseded', 'graduated', 'pruned'));
