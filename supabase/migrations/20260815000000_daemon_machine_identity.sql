-- Daemon machine identity — stop re-registration from minting a duplicate machine.
--
-- THE BUG. POST /api/v1/daemon/register did a blind INSERT keyed on nothing: user_id, a cosmetic
-- device_label, and a token hash. Every re-enrol created ANOTHER row for the same physical machine.
-- Prod on 2026-08-14: 11 rows for 3 machines (MacBook-Air.local x5, monolith x3, mini-6.local x2).
--
-- WHY IT MATTERS. A run pins daemon_id at enqueue. Re-registering therefore STRANDS every run
-- pointed at the old row — nothing listens on that id again, so the run sits at "Starting…" forever
-- while the board reports it as Building. Four runs were stranded on a registration whose last
-- heartbeat was 18.6 days old.

-- ── 1. the identity column ────────────────────────────────────────────────────────────────────────
-- A salted sha256 of the OS machine UUID, computed on the client (machineid.go). Never the raw
-- hardware id. NULLABLE on purpose: a platform that cannot supply one (Windows today, an exotic
-- container) still registers, it just cannot dedupe — which is exactly today's behaviour, so the
-- fallback is a no-op rather than a lockout.
alter table daemons add column if not exists machine_id text;

-- Partial UNIQUE so one machine has at most one live registration per owner. Partial on both
-- columns: rows with no machine_id are exempt (see above), and a REVOKED row must not hold the slot
-- hostage — revoking is how you retire a machine, and it has to leave room to re-enrol.
create unique index if not exists daemons_user_machine_uniq
  on daemons (user_id, machine_id)
  where machine_id is not null and revoked_at is null;

-- ── 2. remediation: re-point runs stranded on a superseded registration ───────────────────────────
-- Scope is deliberately narrow, because a merge that guesses wrong is worse than a duplicate a human
-- can see:
--   * same user_id AND identical device_label — the re-registration signature. Different owners with
--     the same hostname are never merged.
--   * only NON-TERMINAL runs move. A finished run's daemon_id is history and stays truthful; only
--     work that still needs a machine is re-pointed.
--   * the destination is the registration with the most recent heartbeat — the one actually running.
with newest as (
  select distinct on (user_id, device_label) user_id, device_label, id
  from daemons
  where revoked_at is null
  order by user_id, device_label, last_seen desc nulls last
)
update runs r
   set daemon_id = n.id
  from daemons d
  join newest n on n.user_id = d.user_id and n.device_label = d.device_label
 where r.daemon_id = d.id
   and d.id <> n.id
   and r.status in ('queued', 'accepted', 'running', 'needs_approval', 'paused');

-- ── 3. retire the superseded rows ────────────────────────────────────────────────────────────────
-- REVOKED, not deleted: run history still references them, and a soft revoke keeps those foreign
-- keys and the audit trail intact while removing the row from the fleet page and from dispatch.
with newest as (
  select distinct on (user_id, device_label) user_id, device_label, id
  from daemons
  where revoked_at is null
  order by user_id, device_label, last_seen desc nulls last
)
update daemons d
   set revoked_at = now()
  from newest n
 where n.user_id = d.user_id
   and n.device_label = d.device_label
   and d.id <> n.id
   and d.revoked_at is null;
