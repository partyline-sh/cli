-- Conflict-aware review (Slice A2, resolution half). A hidden preset:"rebase" run targets the run
-- whose PR branch needs rebasing — rebase_of points at it, exactly the review_of pattern (its own
-- column, NOT reused, because the run page resolves reviews via review_of and a rebase run must
-- never surface as a review). The daemon rebases the target's branch onto the base, resolves
-- conflicts with a worker pass when needed, force-pushes (with lease), re-scans, and the gate clears.
alter table public.runs add column if not exists rebase_of uuid references public.runs(id);
