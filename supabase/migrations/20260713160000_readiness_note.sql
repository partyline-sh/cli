-- Readiness with receipts: the agent's one-line reason for a work item's readiness score.
-- Written by the shaping agent at finalize/refine (plan-block field `readiness_note`); shown as a
-- tooltip beside the rdy badge. Deliberately NOT human-editable in the UI — override the score, or
-- reopen the session and change the facts.
alter table work_items add column if not exists readiness_note text;
