-- #574 Agent tools: per-project, per-role tool GRANTS for launched agents (planning parties now,
-- build workers in #575). Shape: {"planning": {"mcp": ["linear"], "shell": ["gh *"]}, "build": {...}}.
-- NAMES AND PREFIXES ONLY — pure data. The daemon resolves an mcp NAME against its own local
-- catalog (~/.partyline/mcp.json); commands/env/keys never live here (reference-not-command).
-- The REVIEW agent is deliberately ungrantable: it keeps its read-only checkout tools and never
-- gets producer MCPs (impartiality — verifier ≠ producer).
-- Default '{}' = no grants = exactly today's behavior. Server-side validation caps list sizes and
-- constrains shapes; the web Agent-tools panel and `ptln project tools` are the editors.
alter table public.projects
  add column if not exists agent_tool_grants jsonb not null default '{}'::jsonb;
