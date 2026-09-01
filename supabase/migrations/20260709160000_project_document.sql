-- Project Foundation (Phase B1) — the living project DOCUMENT: a project's globals / rules /
-- guardrails / stack, the CLAUDE.md-equivalent that every run inherits. Stored DB-canonical on the
-- projects row (editable in web, source of truth), later synced OUT to a repo CLAUDE.md and injected
-- into crank runs (Phase B3). One markdown field — it IS the project brief; `description` stays the
-- short one-liner. No version history table (a project_docs child is an additive follow-up if wanted).
--
-- Read via RLS (the existing "projects: read via org" policy covers the new columns); written only
-- through adminClient() after a canMerge check, exactly like display_name/description/repo_url.

alter table public.projects
  add column if not exists document            text not null default '',
  add column if not exists document_updated_at timestamptz,
  add column if not exists document_updated_by uuid references auth.users(id) on delete set null;
