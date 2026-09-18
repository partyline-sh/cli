-- Planning-doc edit queue: ONE pending slot per section. A new proposal for a section now
-- SUPERSEDES any older pending proposal for that same section (it vanishes from the approve
-- queue and is refused if approved anyway) — fixing the live failure where two batches of
-- whole-section snapshots were approved in the wrong order and silently reverted the doc to
-- its pre-decision state. 'superseded' is distinct from 'rejected' (a human's explicit no)
-- so the audit trail stays honest.
alter table public.party_doc_edits drop constraint if exists party_doc_edits_status_check;
alter table public.party_doc_edits add constraint party_doc_edits_status_check
  check (status in ('pending', 'applied', 'rejected', 'superseded'));
