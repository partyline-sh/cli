-- Slice 3d: live-conversation web describe is a PARTY in a new 'describe' mode (see the epic doc).
-- A describe-party is a 1:1 human↔Requirements-Agent conversation whose shared doc (party_documents)
-- becomes a work item in a specific thread's planning tree on Finalize. Two describe-only columns:
--   thread_id     — which thread's planning tree Finalize files the work item into
--   describe_kind — the granularity the finalized item takes (epic | feature | task)
-- Both nullable; a normal party leaves them null. parties.mode is free text (no CHECK), so 'describe'
-- needs no constraint change. describe-parties are filtered out of the general parties list in code.

alter table public.parties
  add column if not exists thread_id     uuid references public.threads(id) on delete set null,
  add column if not exists describe_kind text
    check (describe_kind is null or describe_kind in ('epic', 'feature', 'task'));

-- Finalize links the created work item back to its origin party (audit + "already finalized" guard).
alter table public.work_items
  add column if not exists origin_party_id uuid references public.parties(id) on delete set null;
