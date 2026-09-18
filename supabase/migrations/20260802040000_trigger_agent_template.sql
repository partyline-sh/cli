-- Attach a reusable agent template to a trigger (#836 follow-on).
--
-- ONE TEMPLATE, MANY TRIGGERS. A persona — "triage a failed deploy the way a good engineer would" —
-- is worth authoring once and pointing at from every trigger that needs it. Today the same 827
-- character brief is duplicated across the prod and staging deploy triggers, and the moment someone
-- edits one they drift.
--
-- A SINGLE NULLABLE COLUMN, not a join table. A template is reused across many triggers, but a
-- trigger runs exactly one persona: the many-to-many would be modelling a relationship that does
-- not exist, and would let a trigger end up with two personas and no rule for which wins.
--
-- ON DELETE SET NULL, never CASCADE. Deleting a template must not silently delete the triggers
-- pointing at it — they fall back to their own inline task_template, which still works on its own.
-- A cascade here would turn "tidy up an unused persona" into "delete the deploy triage pipeline".
--
-- Additive and nullable, so every existing trigger behaves exactly as it does now and the previous
-- release runs unchanged against this schema during the container swap.

alter table public.triggers
  add column if not exists agent_template_id uuid
    references public.agent_templates(id) on delete set null;

comment on column public.triggers.agent_template_id is
  'Optional reusable persona (agent_templates) this trigger wakes. The template says WHO the agent '
  'is and what it is for; the trigger''s task_template says WHAT just happened, and carries the '
  '{{fields}} rendered from the caller''s payload. NULL means the inline task alone.';

-- Partial: most triggers will not have one, and the index exists to answer "which triggers use this
-- template" when someone is about to delete or edit it.
create index if not exists triggers_agent_template
  on public.triggers (agent_template_id) where agent_template_id is not null;
