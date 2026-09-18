-- Project threads must be readable by the team that owns the project.
--
-- THE BUG. threads.visibility defaults to 'private', and POST /api/v1/threads/resolve minted a
-- repo's thread without setting it. So a thread keyed to a git remote — the most shared thing a team
-- has, since everyone clones it and they all resolve to this one row — belonged to whoever ran
-- `remember` in that repo first. The RLS policy is
--     (created_by = auth.uid()) OR (visibility = 'team' AND is_org_member(org_id))
-- so for everyone else the row was invisible: their own repo pinned a thread id, and reading it
-- returned "thread not found". Not hypothetical — it cost a day on cyberpunk-game, where the SAME
-- human could not read the thread from their other account, and a second, sibling thread got created
-- for the same repo as a result.
--
-- The forward fix is in the route (it now inserts visibility 'team', with a test that fails if the
-- field is dropped again). This migration repairs the rows already written that way.
--
-- SCOPE, and why widening visibility here is safe rather than presumptuous:
--   * ONLY rows with a project_id. That column means the thread IS a project's thread, which is a
--     team object by construction — threadForProject() has always created these as 'team' itself, so
--     a private one is provably an artifact of the broken path and not somebody's choice.
--   * A thread with no project_id is left ALONE, whatever it is. Personal notes, scratch threads and
--     smoke tests keep their privacy; this migration must never be the reason something private
--     became visible.
--   * org_id is untouched. Visibility widens to the team that already owns the row — never to
--     another team, and never beyond the org boundary.
update threads
   set visibility = 'team'
 where project_id is not null
   and visibility = 'private';
