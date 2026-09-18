-- account_user_id(email) — which partyline account, if any, already owns this address?
-- (Epic one-org-per-user, S4: the invite-collision guard.)
--
-- WHY IT IS NEEDED. An account belongs to exactly one team. Inviting an address that already owns
-- an account therefore creates an invite that can never be usefully accepted: accepting it would
-- give that person a second membership, which is the exact thing this epic exists to remove. Today
-- the invite endpoint has no such check, so it silently issues a pending row and sends the email.
-- That happened in production on 2026-07-28.
--
-- WHY IT IS LOCKED TO service_role. This is an email-existence oracle: "does an account exist for
-- X?", answered for anything you can put in a text field. `emails_for_users` is granted to
-- authenticated and is one of the over-granted RPCs already flagged as a P0 (task #175) — adding a
-- second one would be repeating a mistake we have already written down.
--
-- So EXECUTE is revoked from public/anon/authenticated and granted only to service_role. The only
-- caller is the invite endpoint via adminClient(), which has already proved the caller is an
-- owner/admin of the org and passed the invite rate limit.
-- Returns the owning user's id, or null. The id (rather than a bare boolean) is what lets the
-- caller tell "already on YOUR team" from "on someone else's" — two collisions whose fixes are
-- completely different, and a single "taken" answer would flatten them into one useless message.
create or replace function public.account_user_id(p_email text)
returns uuid
language sql
security definer
set search_path = public, auth
stable
as $$
  select id from auth.users where lower(email) = lower(trim(p_email)) limit 1;
$$;

revoke all on function public.account_user_id(text) from public;
revoke all on function public.account_user_id(text) from anon;
revoke all on function public.account_user_id(text) from authenticated;
grant execute on function public.account_user_id(text) to service_role;
