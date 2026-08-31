-- One org per user: drop `orgs.personal`, and stop naming a team after its owner's email.
-- (Epic one-org-per-user, S1–S3.)
--
-- DEPENDS ON 20260728060000 (drop orgs.slug). The trigger below is rewritten without `slug`, and
-- `orgs.slug` is NOT NULL — so applying this against a database that still has the column would
-- fail every signup. Migrations run in filename order, so this is satisfied by the timestamps as
-- long as both are present.
--
-- WHAT `personal` MEANT AND WHY IT GOES.
-- The old model split "your personal space" from "teams you're in", so a user could hold two
-- memberships and every lookup had to guess which one they meant. That guessing is what #662 was:
-- team users resolved to their EMPTY personal org, so projects/threads/runs looked missing and
-- planning 404'd. One org per user removes the question rather than answering it more carefully.

-- 1 · A team name that is a name.
--
-- `orgs.name` was seeded to the bare email localpart, so the founder's own org is literally called
-- `me`, and a colleague's would be `matthew`.
--
-- Deliberately NOT "<Name>'s Team": a possessive name becomes false the moment a second person
-- joins, and "this is Darcy's space that others visit" is exactly the mental model this epic
-- deletes. A neutral name reads as "rename me" — which the owner now can, since S1–S3 also removes
-- the guard that blocked renaming a personal org.
--
-- No uniqueness needed: two teams may share a name, they are told apart by id.
create or replace function public.generated_org_name()
returns text language plpgsql as $$
declare
  adjectives text[] := array[
    'Amber','Blue','Bright','Copper','Crimson','Golden','Harbor','Iron','Ivory','Northern',
    'Open','Quiet','Rapid','Silver','Slate','Steady','Swift','Umber','Violet','Western'];
  nouns text[] := array[
    'Anchor','Atlas','Basin','Beacon','Bridge','Canyon','Compass','Delta','Ember','Foundry',
    'Junction','Keystone','Lantern','Meridian','Orchard','Quarry','Ridge','Summit','Thicket','Wharf'];
begin
  return adjectives[1 + floor(random() * array_length(adjectives, 1))::int]
      || ' ' ||
      nouns[1 + floor(random() * array_length(nouns, 1))::int];
end $$;

-- 2 · Signup creates ONE org, neutrally named, with no `personal` flag.
--
-- Profile seeding is unchanged here: the `<localpart>-<6hex>` handle stays as the starting value,
-- and S7 is what lets someone change it.
create or replace function public.handle_new_user()
returns trigger language plpgsql security definer set search_path = public as $$
declare
  m    jsonb := coalesce(new.raw_user_meta_data, '{}'::jsonb);
  base text := coalesce(nullif(split_part(new.email, '@', 1), ''), 'user');
  uniq text := base || '-' || substr(replace(new.id::text, '-', ''), 1, 6);
begin
  insert into profiles (id, handle, display_name, avatar_url, github_username)
    values (
      new.id, uniq,
      coalesce(m->>'name', m->>'full_name', base),
      coalesce(m->>'avatar_url', m->>'picture'),
      m->>'user_name'
    );
  insert into orgs (name, created_by)
    values (public.generated_org_name(), new.id); -- org trigger adds owner membership
  return new;
end $$;

-- 3 · The flag goes.
--
-- Existing rows need no data migration: `personal` was only ever read to CHOOSE between two
-- memberships, and S5 (which is done by hand, three rows, deliberately not a blind migration) is
-- what resolves the users who still hold two.
alter table public.orgs drop column if exists personal;
