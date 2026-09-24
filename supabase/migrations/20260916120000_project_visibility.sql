-- Project visibility: 'team' (every org member sees it) or 'private' (only its creator sees it).
--
-- THE FEATURE THIS CARRIES. Adding a project should make it available to the whole team with no
-- further ceremony — machines auto-adopt it by matching repo_url against their own local clones.
-- That only works if "the whole team" is a choice: a person setting up a personal experiment must
-- be able to keep it off everyone else's tray, fleet pickers, and auto-adopt loops. Visibility is
-- that choice, made at creation (default 'team' — sharing is the point of the product) and
-- changeable later by the project's creator, or by an instance admin for any project (enforced at
-- the route with the service role, same posture as personas).
--
-- Backfill: every existing row becomes 'team', which is exactly what the old policy already did —
-- every org member could read every project — so nothing anyone can see changes at migration time.
alter table public.projects
  add column if not exists visibility text not null default 'team'
    check (visibility in ('private', 'team'));

-- The read policy gains the visibility test. Org membership stays the outer wall (a private
-- project is never visible outside its org either); within the org, a private row is readable by
-- its creator alone. Writes are unaffected — they were service-role-only before and stay so.
--
-- Deliberately NOT extended to threads/thread_projects: a private project's plan thread stays
-- org-readable under the threads policies. Visibility gates the project SURFACES (lists, pickers,
-- auto-adopt, tray) — it is a tidiness control, not a secrecy boundary, and pretending otherwise
-- with a half-covered graph would be worse than saying so.
drop policy if exists "projects: read via org" on public.projects;
create policy "projects: read via org"
  on public.projects for select to authenticated
  using (
    public.is_org_member(org_id)
    and (visibility = 'team' or created_by = auth.uid())
  );
