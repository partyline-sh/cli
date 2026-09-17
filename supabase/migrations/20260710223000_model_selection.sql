-- Model selection per phase. No server model key — the worker runs whatever engine/model is installed
-- on it; this just lets the user SELECT which model each phase uses, as DATA forwarded to the engine's
-- --model flag. Engine stays a per-project worker choice (daemon add-project --engine); this is the
-- model WITHIN that engine (claude: opus/sonnet/haiku/fable; codex/gemini: their own).
--
-- projects: a per-phase default profile. Empty = the host/mode default (today's behavior).
--   plan_model   → describe / PRD conversations (best-reasoning is most valuable here)
--   build_model  → crank build runs (the workhorse — cheaper for execution)
--   review_model → the review agent
-- runs.model: the resolved build model for THIS run (defaulted from projects.build_model at enqueue,
--   overridable per run). The daemon forwards it to crank → work --model.
alter table public.projects add column if not exists plan_model text;
alter table public.projects add column if not exists build_model text;
alter table public.projects add column if not exists review_model text;

alter table public.runs add column if not exists model text;
