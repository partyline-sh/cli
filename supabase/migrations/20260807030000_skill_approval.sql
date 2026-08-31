-- SKILLS APPROVAL BOUNDARY (#227/#229, epic #226) — status replaces trust-by-push.
--
-- Until now any org member's `ptln skill push` was live IMMEDIATELY: enabled defaulted true, and
-- the daemon injects enabled skills — scripts included — into every autonomous crank worker's
-- worktree. That made "push a skill" a code-execution path into unattended runs gated only by org
-- membership. The epic's decision (#95): approval is a SECURITY boundary, not UX — an unapproved
-- skill is NEVER surfaced to or run by an agent, the same guarantee context blocks carry (#45).
--
--   proposed  → pushed/suggested, awaiting a human; invisible to every agent surface
--   active    → a human accepted it; callable in crank workers + sessions
--   archived  → declined or retired; kept for provenance, invisible to agents
--
-- Existing skills are grandfathered ACTIVE: they were pushed under the old model by the team that
-- now approves, and retroactively pulling live skills out of running fleets mid-migration would
-- change behavior nobody asked to change. `enabled` remains as the per-skill kill switch UNDER
-- approval (active-but-disabled = temporarily off, no re-approval needed to return).

alter table public.skills
  add column if not exists status text not null default 'proposed'
    check (status in ('proposed', 'active', 'archived')),
  add column if not exists approved_by uuid references auth.users(id) on delete set null,
  add column if not exists approved_at timestamptz,
  -- S.2 provenance: what recurring problem made an agent (or human) propose this. Empty for
  -- direct pushes; shown in review so the approver sees WHY, not just WHAT.
  add column if not exists proposal_reason text not null default '';

update public.skills set status = 'active' where status = 'proposed';
