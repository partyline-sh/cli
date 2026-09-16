-- Project Foundation (Phase B2) — the PROJECT SETUP party. A setup-party is a 1:1 human↔agent
-- conversation (mode 'project_setup', free-text so no CHECK change) whose shared doc IS the project's
-- living globals; Finalize writes party_documents.body → projects.document. One column so Finalize
-- knows which project to write:
--   project_id — the project this setup session fills. Nullable; a normal/describe party leaves it null.
alter table public.parties
  add column if not exists project_id uuid references public.projects(id) on delete set null;
