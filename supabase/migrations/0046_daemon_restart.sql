-- Fleet — owner-triggered restart. A one-shot signal the OWNER sets from the web ("Restart" on
-- the fleet node); the daemon's stream picks it up and, IF it's the supervised always-on service,
-- exits cleanly for launchd/systemd to relaunch the SAME installed binary (no code is fetched —
-- the reference-not-command invariant holds). The stream clears it after emitting (fires once).
-- Owner-only: set via the user session on a daemon you own; never a device token, never a teammate.
alter table public.daemons add column restart_requested_at timestamptz;
