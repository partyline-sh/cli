-- partyline 0009_billing_plan: per-org billing plan + seat count (entitlements
-- foundation, roadmap #1). No behaviour change yet — gates read these columns but
-- EARLY_ACCESS keeps everything Team-level until GA. See docs/BILLING.md.
--
-- Billing is per DISTINCT full-access user, but the plan/subscription lives on the
-- org (the billable entity). `seats` = purchased full-access seats (Stripe qty);
-- viewers are free and never counted. org_members.access (viewer|full) comes in #2.

alter table public.orgs
  add column if not exists plan                  text not null default 'free',
  add column if not exists plan_status           text,            -- stripe: active|trialing|past_due|canceled
  add column if not exists seats                 int  not null default 3,  -- purchased full-access seats
  add column if not exists stripe_customer_id     text,
  add column if not exists stripe_subscription_id text,
  add column if not exists current_period_end    timestamptz;

alter table public.orgs drop constraint if exists orgs_plan_check;
alter table public.orgs add constraint orgs_plan_check check (plan in ('free','team','enterprise'));
