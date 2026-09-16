-- Party modes: a party carries a template ("mode") that sets the room's personality
-- and behavior. `mode` is the template key (chat|incident|review|brainstorm|…),
-- `system_prompt` is the (editable) framing injected into every agent's prompt, and
-- `settings` holds the knobs the runner reads — model + the agent-turn brake. The
-- runner fetches these on connect (GET /parties/[id]/info); CLI flags override.

alter table public.parties
  add column if not exists mode          text not null default 'chat',
  add column if not exists system_prompt text,
  add column if not exists settings      jsonb not null default '{}';  -- {model, maxAgentTurns}
