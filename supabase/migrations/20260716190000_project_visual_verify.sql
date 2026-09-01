-- T2d · VISUAL VERIFY per-project toggle. The visual verify gate ("give crank eyes") renders a
-- task's changed UI and has a vision-capable reviewer look at it before merge. Until now it could
-- only be enabled by committing a `.partyline/visual` render script into the repo. This adds a
-- WEB-CONTROLLABLE toggle so a project owner can flip it on from the Projects UI — the toggle + the
-- routes to screenshot flow to the daemon via the run event, and crank runs the gate for that
-- project's runs (resolving the render HOW from the repo-trusted script or a daemon-hardcoded preset).
--
-- SECURITY: these are the ONLY visual inputs the control plane supplies — a boolean TOGGLE and SAFE
-- route DATA (app paths to screenshot). The web NEVER supplies an executable render script; the
-- render HOW stays repo-trusted or daemon-hardcoded. visual_routes are validated as strict app paths
-- daemon-side before use, so a route can never smuggle a flag or become a command.

alter table public.projects
  add column if not exists visual_verify_enabled boolean not null default false,
  -- SAFE render DATA: app paths (e.g. '/dashboard') the daemon's framework preset screenshots when
  -- the repo has no `.partyline/visual` script. Data only — never executable code. '{}' = the preset
  -- falls back to the app root.
  add column if not exists visual_routes text[] not null default '{}';
