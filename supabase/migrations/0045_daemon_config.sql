-- EPIC Fleet map — #267 (S1). The daemon heartbeat carries a METADATA-ONLY config snapshot so the
-- web can show what each machine is + keep a fresh liveness signal (the heartbeat touches last_seen
-- so a long-lived stream doesn't read as stale). Stored here as jsonb.
--
-- SECURITY: metadata only — CLI version, OS, and advertised projects (label/preset/engine + dir
-- BASENAME). NEVER absolute paths, file contents, MCP URLs, or tokens. The write route rebuilds the
-- snapshot from an explicit allow-list, so even a misbehaving daemon can't persist a secret here.
-- Owner-only via the existing daemons RLS (0023); no new policy needed.
alter table public.daemons add column config jsonb;
