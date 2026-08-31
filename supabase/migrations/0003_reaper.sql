-- The reaper: crashed/killed hosts can't call EndSession; their heartbeats just
-- stop. Every minute, mark any "live" line silent for 3+ minutes as ended.
-- Runs inside Postgres (pg_cron) — no external infra, can't be killed with the host.
-- SUPERSEDED by the app ticker (POST /api/v1/tick, Supabase exit thread #333). The statement below
-- is unchanged and still runs wherever pg_cron exists — production, which has not moved yet — but
-- it is now wrapped so a database WITHOUT the extension (staging, self-hosts, a CI replay against
-- stock postgres:16) skips it instead of failing the whole migration run.
--
-- Not deleted: prod is still on pg_cron and dropping the schedule there would silently disable the
-- job before its replacement exists. It retires when prod is rebuilt on this stack.
do $guard$
begin
    create extension if not exists pg_cron;

    perform cron.schedule(
      'reap-stale-sessions',
      '* * * * *',
      $$ update public.sessions
           set status = 'ended', ended_at = now()
         where status = 'live'
           and last_seen < now() - interval '3 minutes' $$
    );
exception when others then
  raise notice 'pg_cron unavailable — reap-stale-sessions is owned by the app ticker here';
end
$guard$;
