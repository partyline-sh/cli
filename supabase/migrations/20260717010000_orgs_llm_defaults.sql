-- Org-wide default LLM provider (engine) + model — the ROOT of the model/engine cascade a run
-- resolves through at enqueue time:
--
--   task/run override  →  project per-phase pin  →  THIS org default  →  provider preset  →  platform seed
--
-- and NEVER the executor machine's local `claude` default. That machine fall-through is the bug this
-- fixes: with nothing pinned, a build ran at whatever the daemon's `claude` was set to (it was
-- claude-fable-5[1m] — a metered 1M-context mode), which hit "Usage credits are required for this
-- model" ~8 minutes and 2.7M tokens in, and died as a bare "exit status 1". The job now carries an
-- explicit, org-controlled model to the crank task on the daemon, so the operator — not the machine —
-- owns the cost decision.
--
-- default_engine is the closed engine set (blank/null = fall to the provider preset). default_model is
-- free-form (engine-defined; validated for length where accepted). Both null on existing rows = the
-- cascade falls to the provider preset, so this is inert until an org sets a value.
alter table public.orgs
  add column if not exists default_engine text
    check (default_engine is null or default_engine in ('claude', 'codex', 'gemini', 'antigravity')),
  add column if not exists default_model text;

-- 0012_authz_hardening revoked table-wide UPDATE on orgs and re-grants columns explicitly. These two
-- are edited through the same owner/admin PATCH /api/v1/orgs/[slug] via the caller's RLS client, so
-- they need the column-level grant (else 42501 "permission denied for table orgs"). Authorization is
-- unchanged — the orgs UPDATE RLS policy + the PATCH handler both still gate on owner/admin.
grant update (default_engine, default_model) on public.orgs to authenticated;
