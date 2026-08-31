-- Per-project BUILD + REVIEW engines, run-level engine carriage, and the machine's advertised
-- per-project engine. Completes what 20260714170000_plan_engine.sql started: every phase
-- (plan/build/review) can now pin an engine, not just planning. Applied manually to prod.
--
-- projects.build_engine / projects.review_engine: which engine the crank workers / the review
-- agent run for this project (claude / codex / gemini / antigravity). null/'' = the machine's
-- registered default (whatever `daemon add-project --engine` set locally). Takes effect on
-- daemons running CLI >= 0.10.0 (ENGINE_ADAPTER_MIN); older daemons ignore the field and run
-- claude as before.
alter table public.projects add column if not exists build_engine text;
alter table public.projects add column if not exists review_engine text;

-- runs.engine: the resolved engine for THIS run, stamped by the web at enqueue (per-invocation
-- override > the project's phase engine > null). The daemon stream forwards it on the `run`
-- event; the daemon prefers it over its local registry entry when present. null = unset → the
-- daemon uses its machine-local default.
alter table public.runs add column if not exists engine text;

-- daemon_projects.engine: the engine the daemon registered locally for this label (mirrored on
-- PUT /daemon/projects by CLI >= 0.10.0; older CLIs don't send it → null). Display/hint only —
-- the daemon remains the authority on its own machine default.
alter table public.daemon_projects add column if not exists engine text;
