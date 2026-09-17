-- Drop orgs.slug (epic one-org-per-user, S6b).
--
-- The column was a second unique key on `orgs` with no remaining job: not in a single RLS policy,
-- not in any function other than the signup trigger below, never displayed, not editable. It
-- existed for the /org/[slug] page deleted in #187. It also put a piece of the owner's email
-- address (matthew-0e8dd9) into URLs, browser history and server logs.
--
-- ORDERING MATTERS. This must ship only AFTER S6a is live. S6a is the deploy in which the
-- application stops reading the column; dropping it while the previous containers were still
-- serving would break their in-flight `select ... slug` queries mid-rollover.
--
-- Released CLIs are unaffected: GET /api/v1/orgs is the only place a client learns an org "slug",
-- nothing persists one, and since S6a that endpoint serves the org id — which resolves through the
-- uuid branch that already existed. The `org_slug` REQUEST FIELD NAME stays on the wire forever;
-- only the stored column goes.

-- 1 · The signup trigger stops writing it.
--
-- This has to happen in the SAME migration as the drop. The trigger is the only writer left, and a
-- drop without it makes every new signup fail on a missing column — the failure would land on
-- account creation, where nobody would see it until a user reported they could not sign up.
--
-- Unchanged otherwise: profile handle seeding and the OAuth metadata mapping are S7/S2's business,
-- not this migration's. `orgs.name` still gets the email localpart here; S2 replaces that with a
-- generated name.
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
  insert into orgs (name, personal, created_by)
    values (base, true, new.id); -- org trigger adds owner membership
  return new;
end $$;

-- 2 · The column goes. Its unique constraint and index go with it.
alter table public.orgs drop column if exists slug;
