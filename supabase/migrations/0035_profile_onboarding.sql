-- E6 — onboarding state, per account (not per browser): which aha-steps are done, whether
-- the welcome card was dismissed. Tiny jsonb blob owned by the dashboard widget; shape
-- {"dismissed": bool, "steps": {"sessions": bool, "context": bool, "multiplayer": bool}}.
alter table public.profiles add column if not exists onboarding jsonb not null default '{}'::jsonb;
