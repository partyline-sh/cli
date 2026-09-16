-- Epic G.0 — the typed gate report on each task.
--
-- Until now the verify gate's whole output on a task was `detail`, a prose string. That is why
-- nothing downstream could tell a rate-limited reviewer from a rejected diff, why a second reviewer
-- lane had nowhere to live, and why the board could only ever render one amber "needs approval"
-- card for four unrelated situations.
--
-- `gate` is the full typed report (internal/gate.Report, versioned). `gate_verdict` and
-- `gate_class` are the two fields the control plane FILTERS and GROUPS by, lifted out into real
-- columns so the board does not have to scan jsonb to draw a column.
--
-- INVARIANT, unchanged: these rows are written ONLY by the owning daemon via the service role
-- (device token). There is no authenticated write policy on run_tasks and this migration does not
-- add one. The control plane still holds DATA, never commands.
--
-- Backwards compatible on purpose. Every column is nullable with no default beyond NULL, so a
-- daemon that predates this migration keeps reporting exactly as it does today and its tasks simply
-- carry no report. A NULL gate means "this daemon did not tell us", which is honestly different
-- from `skipped` ("the gate ran and nothing was enabled") — and the UI must not conflate them.

alter table public.run_tasks
  add column if not exists gate jsonb,
  add column if not exists gate_verdict text,
  add column if not exists gate_class text;

-- The vocabulary is generated from internal/surface (S.1). Adding a value here without adding it
-- there — or the reverse — is the exact failure #195 recorded for run presets, so the two are
-- generated from one declaration and TestConstantsAreDeclaredTerms guards the Go side.
alter table public.run_tasks drop constraint if exists run_tasks_gate_verdict_check;
alter table public.run_tasks add constraint run_tasks_gate_verdict_check
  check (gate_verdict is null or gate_verdict in
    ('pass', 'pass_with_findings', 'fail', 'blocked', 'skipped'));

alter table public.run_tasks drop constraint if exists run_tasks_gate_class_check;
alter table public.run_tasks add constraint run_tasks_gate_class_check
  check (gate_class is null or gate_class in ('none', 'transient', 'hard'));

-- The board asks "which tasks in this run were quarantined?" on every render of a paused run.
-- Partial, because the rows worth indexing are the small minority that failed.
create index if not exists run_tasks_gate_verdict_idx
  on public.run_tasks (run_id, gate_verdict)
  where gate_verdict is not null and gate_verdict <> 'pass';

comment on column public.run_tasks.gate is
  'The full typed verify-gate report (internal/gate.Report). NULL means the reporting daemon predates Epic G.0 — distinct from a verdict of ''skipped'', which means the gate ran and nothing was enabled.';
comment on column public.run_tasks.gate_verdict is
  'Lifted from gate->>''verdict'' so the board can filter without scanning jsonb.';
comment on column public.run_tasks.gate_class is
  'Retry disposition of the failure, when there was one: none | transient | hard. Transient failures are retried without a human.';
