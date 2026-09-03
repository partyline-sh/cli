-- Review gate — a per-team switch (ON by default) that makes every finished run wait in the board's
-- Review column until a human clicks Accept, instead of some runs auto-landing in Shipped. Also caps
-- new runs' merge policy at `pr` while ON (enforced in web enqueue) so nothing auto-merges before the
-- human sign-off. Org-scoped because every run has org_id (the hard team wall) — one flag covers all
-- runs, including those on unpromoted labels that have no project row.
alter table public.orgs add column if not exists require_review boolean not null default true;

-- Grandfather existing history so turning the gate ON (default true) doesn't yank already-"shipped"
-- runs back into Review on first load. A `done` run that opened a PR is what the board shows as
-- Shipped today; stamp it accepted_at so it STAYS shipped. Runs currently in Review (done-no-PR,
-- failed, needs_approval) are untouched — they still demand a look. Only runs that COMPLETE after
-- this migration get newly gated.
update public.runs
   set accepted_at = coalesce(decided_at, created_at)
 where status = 'done'
   and accepted_at is null
   and exists (select 1 from public.run_tasks t where t.run_id = runs.id and t.pr_url is not null);
