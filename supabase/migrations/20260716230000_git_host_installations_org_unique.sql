-- One git-host connection per org per host.
--
-- The original table had `unique (host, installation_id)` with the callback upserting on that pair.
-- But a GitHub App RE-install issues a BRAND-NEW installation_id, so the conflict target never
-- matched and each reconnect INSERTED a second row for the same org. getOrgInstallation() then read
-- with .maybeSingle(), which errors (→ null) on multiple matches — so Settings showed "Connect
-- GitHub" for an org that was actually connected (seen on org a75618e5: two `github` rows).
--
-- Fix the invariant at the schema: an org has at most ONE installation per host, and a reconnect
-- replaces it. (Companion code: recordInstallation now upserts onConflict (org_id, host);
-- getOrgInstallation is also hardened to newest-wins so a stray dup can never blank the UI again.)

-- 1) Dedup existing rows: keep the newest installation per (org_id, host), drop the stale ones.
--    Tie-break on id so identical created_at can't leave a duplicate behind.
delete from public.git_host_installations g
using public.git_host_installations newer
where g.org_id = newer.org_id
  and g.host = newer.host
  and (g.created_at, g.id) < (newer.created_at, newer.id);

-- 2) Replace the (host, installation_id) uniqueness with the real business rule (org_id, host).
--    Drop the old inline-named constraint by introspection (its auto-generated name is
--    git_host_installations_host_installation_id_key, but resolve it defensively).
do $$
declare
  cname text;
begin
  select conname into cname
  from pg_constraint
  where conrelid = 'public.git_host_installations'::regclass
    and contype = 'u'
    and conkey = (
      select array_agg(attnum order by attnum)
      from pg_attribute
      where attrelid = 'public.git_host_installations'::regclass
        and attname in ('host', 'installation_id')
    );
  if cname is not null then
    execute format('alter table public.git_host_installations drop constraint %I', cname);
  end if;
end $$;

alter table public.git_host_installations
  add constraint git_host_installations_org_host_key unique (org_id, host);
