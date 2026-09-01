-- Per-project PLANNING engine + server-authoritative engine on launch requests.
--
-- projects.plan_engine: which engine the describe/planning agent runs for this project
-- (claude / codex / gemini / antigravity). Planning is the ONE phase all four engines support
-- today, so it's the only per-project engine choice. null/'' = the machine's registered
-- default (whatever `daemon add-project --engine` set locally). BUILD and REVIEW workers stay
-- hardcoded claude (claude-specific tool flags) until the engine-adapter epic — deliberately
-- NO build_engine/review_engine columns here.
alter table public.projects add column if not exists plan_engine text;

-- launch_requests.engine: the resolved planning engine for THIS launch, stamped by the web at
-- insert (from projects.plan_engine, org+label scoped). The daemon stream forwards it on the
-- `accepted` event; the daemon prefers it over its local registry entry when present. null =
-- unset → the daemon uses its machine-local default.
alter table public.launch_requests add column if not exists engine text;
