-- Auto-resume-at-reset (Resume — the bonus). A rate-limited crank run PAUSES as `needs_approval`
-- carrying runs.resume_at (the provider's quota-window reset; set by crank's SetRunPaused, 0056).
-- Slice 2 gave the human a one-click "Resume now" once the window passes. This adds the AUTOMATIC
-- twin so the common interruption (a 5-hour Anthropic rate limit) doesn't need a babysitter: once
-- the window has reset, a pg_cron job flips the paused run to `accepted` — the EXACT status the
-- "Resume now" button sets — so the always-on daemon re-picks it headlessly (acceptedRuns → go=true)
-- and runs `crank --resume` (resume-in-place from the stored session, restart:false by default).
--
-- Why this is the whole feature: everything downstream of an `accepted` flip is already wired, so
-- auto-resume reduces to "flip the status when the clock says it's safe."
--
--   • Discriminator — ONLY rate-limit pauses carry resume_at. Budget/quarantine pauses (resume_at
--     NULL) genuinely need human judgment and are DELIBERATELY left untouched; the predicate's
--     `resume_at is not null` guarantees the job never auto-resumes those.
--   • Don't jump the wall — `resume_at <= now()` means we only proceed AFTER the quota resets;
--     resuming earlier would immediately re-throttle and re-pause (the anti-pattern this fixes).
--   • Fleet-safe + idempotent — the DB is the single arbiter. The UPDATE is status-guarded
--     (`status = 'needs_approval'`), so once flipped to `accepted` the row no longer matches: the
--     cron and a human "Resume now" cannot double-dispatch (whoever flips first wins; the other's
--     WHERE stops matching). resume_at is cleared later by the daemon's `running` transition.
--   • Reference-not-command — the job only changes a status column; the daemon still resolves the
--     label against its own registry and runs ITS OWN engine binary. No code crosses the plane.
--
-- Runs inside Postgres (pg_cron) — no external infra, survives a daemon/host restart. Cadence 1/min
-- matches the reaper (0003): reset-to-resume latency is at most ~60s, which is nothing against a
-- 5-hour window.
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
      'auto-resume-runs',
      '* * * * *',
      $$ update public.runs
           set status = 'accepted', decided_at = now()
         where status = 'needs_approval'
           and resume_at is not null
           and resume_at <= now() $$
    );
exception when others then
  raise notice 'pg_cron unavailable — auto-resume-runs is owned by the app ticker here';
end
$guard$;
