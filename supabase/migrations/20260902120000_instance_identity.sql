-- INSTANCE IDENTITY — the name a client can keep using after the URL changes.
--
-- Client config (tokens, device enrolment, the TLS pin) was keyed by HOSTNAME: ~/.partyline/envs/<host>/.
-- So moving an instance from https://192.168.1.170:8443 to https://partyline.example.com made every
-- machine in the fleet a stranger to it — enrolment gone, tokens orphaned, daemons retrying a dead
-- endpoint forever. The hostname is not the instance; it is one of the addresses the instance
-- currently answers on.
--
-- WHY THE DATABASE AND NOT THE CONTAINER. The identity has to survive exactly what self-hosters
-- actually do: destroy the container, pull a new image, restore the volume. A UUID minted into a
-- config file (or an env var in a compose file) is lost on every rebuild, which would convert a
-- routine upgrade into a fleet-wide outage — strictly worse than keying on the hostname. Living in
-- the singleton row means it rides any dump/restore, and a genuinely fresh database correctly mints
-- a new one. Containers stay cattle; identity follows the data, which is the thing being backed up.
--
-- NOT A SECRET. It is served unauthenticated at /.well-known/partyline so a client can ask "are you
-- still the instance I was enrolled with?" before it holds any credential. It authorises nothing: a
-- client that knows this id still has to complete the device flow. Treat it as a name, not a key —
-- which is why the ADDRESS a client should use is never taken from an unauthenticated source.
alter table public.instance_settings
  add column if not exists instance_id uuid not null default gen_random_uuid();

-- Backfill is implicit: Postgres 11+ populates existing rows from the volatile default, and each
-- row gets a distinct value. There is exactly one row (the singleton check constraint), so the
-- existing install keeps one stable id from this migration forward.

comment on column public.instance_settings.instance_id is
  'Stable identity for this deployment, minted once and carried by any backup/restore. Clients key their local config by this instead of the hostname, so changing the site URL does not orphan enrolled machines. Public, and authorises nothing.';
