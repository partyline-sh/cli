-- EPIC A1 — the shared working surface. One markdown doc per party that humans + agents
-- co-edit: agents PROPOSE section-scoped edits, humans APPROVE/merge. The edit log is the
-- audit trail. Coordination artifact (not E2EE; same trust model as party_messages).
-- All writes are service-role (the backend mediates every change); RLS gives org members
-- + the creator read access, mirroring party_messages.

-- One document per party (party_id is the PK). Created lazily on first read, or seeded
-- from the party mode's template on create (A1.6). version drives optimistic-concurrency
-- merges (A1.4).
create table public.party_documents (
  party_id   uuid primary key references public.parties(id) on delete cascade,
  body       text not null default '',
  version    integer not null default 1,
  updated_at timestamptz not null default now()
);
alter table public.party_documents enable row level security;

create policy "party_documents: read via party"
  on public.party_documents for select to authenticated
  using (exists (select 1 from public.parties p
                 where p.id = party_id
                   and (public.is_org_member(p.org_id) or p.created_by = auth.uid())));

-- Proposed/decided section edits — the pending-change queue AND the audit trail. Each row
-- is a proposal against a (section, base_version); a human approves (→ merged into the doc,
-- applied) or rejects. Service-role writes only.
create table public.party_doc_edits (
  id           bigint generated always as identity primary key,
  party_id     uuid not null references public.parties(id) on delete cascade,
  section      text not null,                              -- the "## <section>" it targets
  author       text not null,                             -- 'user:<handle>' | 'agent:<name>'
  new_body     text not null,                             -- proposed replacement for the section
  base_version integer not null,                          -- doc version the proposal was made against
  status       text not null default 'pending'
               check (status in ('pending', 'applied', 'rejected')),
  decided_by   uuid references auth.users(id) on delete set null,
  decided_at   timestamptz,
  created_at   timestamptz not null default now()
);
alter table public.party_doc_edits enable row level security;

create policy "party_doc_edits: read via party"
  on public.party_doc_edits for select to authenticated
  using (exists (select 1 from public.parties p
                 where p.id = party_id
                   and (public.is_org_member(p.org_id) or p.created_by = auth.uid())));

create index party_doc_edits_party_created on public.party_doc_edits (party_id, created_at);
create index party_doc_edits_pending on public.party_doc_edits (party_id) where status = 'pending';

-- Doc events ride the existing party_messages fan-out (Redis/SSE) so every surface updates
-- live — add the 'doc' kind to the allowed set.
alter table public.party_messages drop constraint if exists party_messages_kind_check;
alter table public.party_messages add constraint party_messages_kind_check
  check (kind in ('msg', 'status', 'ask', 'system', 'doc'));
