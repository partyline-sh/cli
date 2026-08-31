-- New-project scaffold (Slice 1) — git-host App installations.
--
-- Provider-agnostic by design (github now; gitlab / bitbucket later): we store WHICH host and the
-- host's own installation id, keyed to the partyline org that owns it. We deliberately store NO
-- token — installation access tokens are short-lived (≈1h) and minted on demand from the App's
-- private key, which lives ONLY in the deployment secret store, never the DB. The durable record
-- here is just "org X has installation Y on host Z", plus display metadata (which GitHub account the
-- App is installed on).
--
-- Read via RLS (members of the owning org see "connected"); written only through adminClient() after
-- the install-callback validates the session user owns the org — same posture as projects.

create table if not exists public.git_host_installations (
  id              uuid primary key default gen_random_uuid(),
  org_id          uuid not null references public.orgs(id) on delete cascade,
  host            text not null default 'github' check (host in ('github', 'gitlab', 'bitbucket')),
  installation_id text not null,          -- the host's installation id (text = provider-agnostic)
  account_login   text,                   -- the GitHub org/user the App is installed on (display)
  account_type    text,                   -- 'Organization' | 'User' (display)
  installed_by    uuid references auth.users(id) on delete set null,
  created_at      timestamptz not null default now(),
  updated_at      timestamptz not null default now(),
  unique (host, installation_id)          -- one row per real installation; re-install upserts
);

alter table public.git_host_installations enable row level security;

-- Members of the owning org can SEE the installation (so Settings shows "GitHub connected").
create policy "git_host_installations: read via org"
  on public.git_host_installations for select to authenticated
  using (
    exists (
      select 1 from public.org_members m
      where m.org_id = git_host_installations.org_id and m.user_id = auth.uid()
    )
  );

-- No authenticated INSERT/UPDATE/DELETE policy — writes go through adminClient() after the callback
-- proves the session user owns the org (owner/admin), exactly like projects.

create index git_host_installations_org on public.git_host_installations (org_id);
