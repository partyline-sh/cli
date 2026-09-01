-- PROJECTS · canonical layer (Phase 1). The Projects list is currently derived bottom-up: any
-- LABEL a daemon advertises, a run targets, or a projects row carries auto-surfaces as a card.
-- That gives zero-setup discovery but no authoritative, editable definition. This adds the
-- top-down layer: a canonical `projects` row you can name, describe, tag with source/repo metadata,
-- promote (from an advertised label), edit, and delete — while `label` stays the immutable JOIN KEY
-- across daemon_projects / runs / thread_projects (renaming it is a cascade, handled in Phase 2).

alter table public.projects
  add column if not exists display_name text,                       -- human name; falls back to label
  add column if not exists description  text,
  add column if not exists source       text not null default 'web' -- 'web' (defined here) | 'promoted' (from an advertised label)
    check (source in ('web', 'promoted')),
  add column if not exists repo_url      text;                      -- git remote / where it lives (curated in Phase 1)

-- label is the identity of a project WITHIN an org (the join key the fleet/runs aggregate on), so
-- it must be unique per org. Promote/create rely on this to be idempotent. If this fails on apply,
-- an org has duplicate project labels that must be de-duped first.
create unique index if not exists projects_org_label_uq on public.projects (org_id, label);
