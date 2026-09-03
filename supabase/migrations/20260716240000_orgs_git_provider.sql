-- The org's ACTIVE code-repository provider. One at a time, to keep the mental model simple: an org
-- runs GitHub *or* GitLab *or* Bitbucket, and every provider-specific message/instruction in the app
-- keys off this. Only GitHub has a brokered integration (the GitHub App — short-lived tokens, nothing
-- stored). GitLab/Bitbucket are "selected" providers that surface self-service workaround instructions
-- rather than a stored-credential vault (a deliberate no-vault decision).
alter table public.orgs
  add column if not exists git_provider text not null default 'github'
    check (git_provider in ('github', 'gitlab', 'bitbucket'));
