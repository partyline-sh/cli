-- MULTI-ORG GitHub — let ONE partyline org connect MORE THAN ONE GitHub org. Different projects
-- often live under different GitHub orgs, and a GitHub App installation grants access to exactly one
-- GitHub account/org — so reaching several means storing several installations (one per GitHub org),
-- each already distinguished by its account_login. 20260716230000 narrowed this table to
-- unique(org_id, host) — one install per partyline org — which is precisely the cap that made
-- multi-org impossible. Undo that: go back to unique(host, installation_id) (one row per REAL
-- installation), so a partyline org can hold N installs and the token-mint path routes by the repo's
-- owner (getRepoInstallation) to the matching one.
--
-- No data migration needed: existing single-install orgs keep their one row (it just stops being the
-- only one allowed), and getRepoInstallation falls back to the sole install when an org has exactly
-- one — so single-org setups are byte-for-byte unchanged.
alter table public.git_host_installations drop constraint if exists git_host_installations_org_host_key;
alter table public.git_host_installations drop constraint if exists git_host_installations_host_inst_key;
alter table public.git_host_installations add constraint git_host_installations_host_inst_key unique (host, installation_id);
