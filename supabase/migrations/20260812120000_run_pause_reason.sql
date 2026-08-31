-- Epic G.2 — say WHY a run is paused.
--
-- `needs_approval` means "a human may be needed". It does not say what for, and five unrelated
-- situations arrive at it:
--
--   budget       the token ceiling was reached at a task boundary
--   rate_limit   the provider throttled us; a scheduled job resumes at resume_at
--   entitlement  the provider refused on BILLING (credits, overage, model not enabled)
--   quarantine   a verify gate rejected at least one task
--   stall        the run stopped producing output
--
-- Collapsing them cost real time. A rate-limit pause needs NO human action at all — auto-resume
-- already handles it — but it rendered as the same amber "needs approval" card as a quarantine,
-- which is where "this seems to be waiting for a decision but there's nothing to decide" came
-- from. And entitlement is deliberately its own value rather than a flavour of rate_limit: a quota
-- reset clears a rate limit, whereas only a human changing billing clears an entitlement block, so
-- offering a countdown there sends the operator to wait for a moment that never arrives. crank
-- already draws that distinction in prose (crank.go, maybePauseForRateLimit); this makes it data.
--
-- The information already exists at the source — crank exits 3 (budget), 4 (quarantine), 5 (rate
-- limit / entitlement), and the web computes stall. It was thrown away at the boundary. This column
-- is where it survives.
--
-- Nullable, no default: an older daemon keeps reporting exactly as it does today and its pauses
-- carry no reason. NULL means "we were not told", which the UI must render as the current generic
-- card rather than guessing — a wrong guess here is precisely the bug being fixed.

alter table public.runs
  add column if not exists pause_reason text;

alter table public.runs drop constraint if exists runs_pause_reason_check;
alter table public.runs add constraint runs_pause_reason_check
  check (pause_reason is null or pause_reason in
    ('budget', 'rate_limit', 'entitlement', 'quarantine', 'stall'));

-- The board groups paused runs by reason on every render of the Build column.
create index if not exists runs_pause_reason_idx
  on public.runs (pause_reason)
  where pause_reason is not null;

comment on column public.runs.pause_reason is
  'Why a needs_approval run is paused: budget | rate_limit | entitlement | quarantine | stall. NULL = the reporting daemon predates Epic G.2 and did not say. rate_limit needs no human action (auto-resume handles it); entitlement cannot be resolved by waiting, only by changing billing.';
