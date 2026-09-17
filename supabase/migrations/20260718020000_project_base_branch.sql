-- The branch a project's work forks FROM and opens PRs INTO.
--
-- Until now this was entirely implicit and unchangeable: gitwt rooted every new branch at
-- `origin/HEAD` (whatever GitHub calls the repo default) and `gh pr create` was invoked with no
-- `--base`, so it fell back to the same thing. A team that merges to `develop` or `staging` had no
-- way to say so.
--
-- ONE column drives BOTH the fork point and the PR base, deliberately. They must be the same ref:
-- fork from `main` but target `staging` and the PR shows every commit that differs between the two
-- branches, not the run's work. Two independent knobs would make that misconfiguration reachable.
--
-- Distinct from repo_default_branch (added in the provisioned-workers migration), which is a FACT
-- resolved from GitHub at verify time — "what the repo says". This is the operator's CHOICE.
-- null → fall back to repo_default_branch → the daemon's own origin/HEAD (today's behavior).
alter table public.projects
  add column if not exists base_branch text;

comment on column public.projects.base_branch is
  'Branch that runs fork from and open PRs into. null = repo_default_branch, then origin/HEAD.';
