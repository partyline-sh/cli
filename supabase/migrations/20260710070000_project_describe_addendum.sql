-- Project-scoped DESCRIBE INSTRUCTIONS — an optional block of extra guidance a team pins to a project
-- that gets APPENDED to the default Requirements-Agent prompt for every describe conversation in that
-- project. It never replaces the default (which owns the plan/JSON contract) — it adds to it, so a team
-- can say "always propose a migration plan", "prefer server components", "ask about auth" once instead
-- of re-typing it each time. Mirrors projects.document (the run globals); read at describe-party create
-- time in /api/v1/describe/converse and folded into the party's system_prompt.
alter table public.projects
  add column if not exists describe_prompt_addendum text;
