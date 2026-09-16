-- partyline 0011_signup_notify: one-shot "new signup" Slack alert flag.
-- The web app fires a Slack message the first time a new user is seen (first
-- authenticated request) and flips this flag so it only ever fires once.
-- Backfill existing users to TRUE so we don't alert for accounts that already exist.

alter table public.profiles
  add column if not exists signup_notified boolean not null default false;

update public.profiles set signup_notified = true where signup_notified = false;
