-- AUTO-MERGE ON ACCEPT — a per-project toggle. When ON, the human's Accept on a Review card doesn't
-- just flip the draft PR to ready (markPullRequestReadyForReview) — it also SQUASH-MERGES the PR via
-- the org's GitHub App token, so Accept is the one action that ships the work. When OFF (the default,
-- today's behavior) Accept only marks the PR ready and a human merges it on GitHub.
--
-- Project-scoped (not org) because merge posture is a per-repo decision: a repo with a manual merge
-- queue / strict branch protection wants OFF, while a fast-moving repo wants Accept to just ship.
-- Default false keeps every existing project on the current safe behavior until a project owner opts
-- in from the project settings page. The merge is still gated by GitHub itself (mergeability, required
-- checks) — the toggle only decides whether partyline ATTEMPTS the merge on Accept.
alter table public.projects
  add column if not exists auto_merge_on_accept boolean not null default false;
