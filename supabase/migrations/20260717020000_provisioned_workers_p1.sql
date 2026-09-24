-- Provisioned workers, P1 (docs/plans/provisioned-workers.md): structured repo identity so a run can
-- be dispatched to a daemon that has NO local clone — the daemon pulls the repo from GitHub at job
-- time. Today `projects.repo_url` (0049) is free-text display metadata that nothing daemon-facing
-- reads; provisioning needs a machine-usable identity: owner/name (to clone + name the managed dir),
-- the GitHub numeric id (to narrow the minted App token to just this repo — least privilege, the
-- `repositoryIds` param already waiting in git-hosts.ts), and the default branch (the clone/branch
-- base). All nullable — a project without them simply can't be provisioned (the enqueue gate rejects
-- it with a clear error), so this is inert until curated.
alter table public.projects
  add column if not exists repo_full_name text,  -- "owner/name" — validated shape server-side before use
  add column if not exists repo_id bigint,       -- GitHub numeric repo id — enables repo-scoped clone tokens
  add column if not exists repo_default_branch text; -- clone/branch base; "" / null → resolved at verify time

-- Mark a run that rode the PROVISION dispatch path (dispatched to a daemon that lacks the local repo
-- and must clone it). The daemon reads this off the run event to choose provision-vs-registry
-- resolution; the board/audit trail reads it to explain "this ran on a fetched clone". Default false =
-- every existing and normal run is unchanged.
alter table public.runs
  add column if not exists provisioned boolean not null default false;

-- The pool/provision opt-in for a daemon rides the existing heartbeat `daemons.config` jsonb (added to
-- the heartbeat route's explicit allow-list in this PR) — no column needed, matching how `candidates`
-- and `version` already travel. No grants needed: the project PATCH writes via the service-role admin
-- client (updateProject → adminClient), and runs.provisioned is written service-role at enqueue.
