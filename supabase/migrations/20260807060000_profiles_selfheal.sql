-- PROFILES: heal the missing row, and let it be created at all.
--
-- THE BUG. Settings → Profile said "Saved" and the handle was gone on refresh. Not a UI bug: the
-- account had NO row in public.profiles, and `update ... where id = auth.uid()` matching zero rows
-- is a SUCCESS in PostgREST — no error, nothing written. Every read (me_with_plan, `ptln whoami`)
-- then returned nothing, which rendered as an empty handle and "(not set)". The write, the read
-- and the UI were each behaving correctly; the row simply wasn't there.
--
-- `handle` is `not null`, so an empty handle can ONLY mean a missing row — that is the fingerprint.
--
-- WHY A ROW CAN BE MISSING. handle_new_user (0001_core) seeds one at signup, so accounts created
-- before that trigger — or whose auth.users row was recreated (email change / provider relink,
-- which mints a new uuid) — have an auth user with no profile. Nothing since has repaired them,
-- and nothing could: there was no INSERT policy on profiles, so not even the owner could create
-- their own row. Two holes, closed together.

-- ============================================================ 1. let a user create their own row
-- SELECT (self-or-co-member) and UPDATE (own) policies exist; INSERT was never granted, so an
-- upsert from the app's RLS-scoped client failed. Self only — the check pins the row to the caller.
drop policy if exists "profiles: own insert" on public.profiles;
create policy "profiles: own insert"
  on public.profiles for insert to authenticated
  with check (auth.uid() = id);

-- ============================================================ 2. backfill every missing row
-- Same handle shape the signup trigger uses (<email-localpart>-<6 hex of the uid>), so a healed
-- account is indistinguishable from a freshly created one. ON CONFLICT guards the race with a
-- concurrent signup; the uid suffix makes a collision essentially impossible anyway.
insert into public.profiles (id, handle)
select u.id,
       coalesce(nullif(split_part(u.email, '@', 1), ''), 'user')
         || '-' || substr(replace(u.id::text, '-', ''), 1, 6)
from auth.users u
left join public.profiles p on p.id = u.id
where p.id is null
on conflict (id) do nothing;
