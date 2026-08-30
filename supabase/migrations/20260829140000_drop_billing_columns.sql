-- Drop the billing columns. partyline is self-hosted; there is nothing to charge for.
--
-- 0009_billing_plan added these as the "entitlements foundation" for a hosted service that has
-- since been retired: the Stripe integration is gone, the /pricing page is gone, the billing routes
-- are gone, and the npm package went with them. What was left was six columns on `orgs` that no
-- code reads — verified before writing this: no hit for stripe_customer_id, stripe_subscription_id,
-- plan_status or current_period_end anywhere in web/src or the Go tree.
--
-- WHY DROP RATHER THAN LEAVE THEM. This repository is being opened. Columns named
-- stripe_customer_id on a product with no billing are a question every reader has to ask and
-- answer for themselves, and a schema is documentation whether or not it is meant to be.
--
-- `plan` and `seats` go too. They are NOT NULL with defaults, so nothing breaks by their absence,
-- and the thing they encoded — who may use this instance — is now answered by
-- instance_settings.allow_signups and org membership. A "free/team/enterprise" tier on a box you
-- run yourself is a distinction without a difference.
--
-- IF YOU ARE RESTORING A BACKUP TAKEN BEFORE THIS RAN, the columns come back with it and this
-- migration is idempotent — `if exists` on every drop, so re-running is safe.

alter table public.orgs drop constraint if exists orgs_plan_check;

alter table public.orgs
  drop column if exists plan,
  drop column if exists plan_status,
  drop column if exists seats,
  drop column if exists stripe_customer_id,
  drop column if exists stripe_subscription_id,
  drop column if exists current_period_end;

-- The email-change helper that only ever existed to resolve a WorkOS user id. WorkOS is gone as an
-- auth provider; grep finds no caller anywhere in the tree. Dropped here rather than left as a
-- service_role-only function nobody invokes.
drop function if exists public.workos_id_for_user(uuid);
