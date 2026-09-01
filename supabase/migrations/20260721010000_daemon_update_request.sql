-- Web-triggered daemon update (version locking, slice V1). One-shot signal column, exactly the
-- restart_requested_at pattern: an org member clicks "update" on the fleet page → the flag is set →
-- the daemon's stream drains it once (consumeUpdateRequest) and emits a `type:"update"` event → the
-- daemon runs its OWN guarded upgrade path (same public release + installer every install uses;
-- service-managed, no runs in flight, newer-only). The control plane never ships code or artifact
-- URLs — this column is a nudge, not a payload. Reference-not-command holds.
alter table public.daemons add column if not exists update_requested_at timestamptz;
