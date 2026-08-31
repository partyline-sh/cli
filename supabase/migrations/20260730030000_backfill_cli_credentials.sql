-- Edge E1 phase 3a (#749): backfill every legacy CLI token into edge_credentials.
--
-- This is the step that makes dropping the api_tokens fallback survivable. Until every legacy row
-- has a partner here, the fallback in resolveCliToken is the ONLY thing authenticating those
-- people, and removing it signs them out permanently.
--
-- IT NEEDS NO SECRETS, which is the whole reason it can be a migration rather than a coordinated
-- re-login: we never stored raw tokens anywhere. api_tokens.token_hash and edge_credentials.hash
-- are the same sha256 of the same value, so copying the hash is sufficient for the new table to
-- resolve exactly the tokens the old one did.
--
-- Idempotent: re-running inserts nothing (WHERE NOT EXISTS on the unique hash), so it is safe to
-- replay, safe to run twice from two replicas, and safe to leave in the migration history.

insert into public.edge_credentials (kind, user_id, name, prefix, hash, scopes, created_by, created_at, last_used_at)
select
  'user_cli',
  t.user_id,
  coalesce(nullif(t.name, ''), 'cli'),
  -- The prefix is derived from the RAW value, which we do not have and never did. A placeholder is
  -- the honest answer: a migrated token's first bytes are genuinely unknown, and inventing one
  -- would put a wrong value in the field used to identify a leaked key. Tokens minted from here on
  -- carry a real prefix; these will simply not match a secret scan, which is a known gap and better
  -- than a false one.
  'plt_(migrated)',
  t.token_hash,
  -- Exactly what a personal token carries today. The migration must not silently widen OR narrow
  -- anyone's authority — it is a change of storage, not a change of permission.
  array['cli:full'],
  -- created_by is audit only ("who minted this"), and for a legacy row the only truthful answer is
  -- the owner. It confers nothing: a credential never acts as created_by.
  t.user_id,
  t.created_at,
  t.last_used
from public.api_tokens t
where not exists (
  select 1 from public.edge_credentials c where c.hash = t.token_hash
);

-- After this, /api/v1/admin/credential-migration should report legacy_only = 0 and
-- safe_to_drop_fallback = true. Phase 3b removes the fallback — in a LATER release, never this one:
-- migrations apply BEFORE containers recreate, so the code that still reads api_tokens must keep
-- working for the length of the swap window.
