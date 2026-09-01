-- Epic G.3 — a worker cannot mark its own work verified.
--
-- crank self-reports each task's lifecycle, including `done`. The threat that actually happens is
-- not a malicious daemon; it is an LLM deciding it is finished because it feels finished. Moving
-- the VERDICT out of the model's reach is the point.
--
-- THE COMPATIBILITY CONSTRAINT, and why this is not the absolute rule the epic first described.
-- A daemon older than the release that sends gate reports sends none at all. "A worker may never
-- report done" would break every deployed client the moment this lands. So the rule is conditional
-- and states its own limit honestly:
--
--     if a task carries a gate verdict, `done` must AGREE with it
--     if it carries none, `done` is accepted and the task is recorded as UNVERIFIED
--
-- gate_verdict NULL therefore means "nobody told us", which is different from `skipped` ("the gate
-- ran and nothing was enabled") and different again from `pass`. Three distinct facts, three
-- distinct values — the UI must not collapse them, because "we did not check" reading as "it
-- passed" is the whole failure being prevented.
--
-- Defence in depth: the API route enforces this too. The trigger exists because the route is one
-- refactor away from being bypassed, and this is the last line before a lie is durable.

create or replace function public.run_task_done_needs_a_passing_gate()
returns trigger
language plpgsql
as $$
begin
  if new.status = 'done'
     and new.gate_verdict is not null
     and new.gate_verdict not in ('pass', 'pass_with_findings') then
    raise exception
      'run_tasks: task %/% cannot be done — its gate verdict is %. A task whose gate rejected it is blocked, not done.',
      new.run_id, new.idx, new.gate_verdict
      using errcode = 'check_violation';
  end if;
  return new;
end;
$$;

drop trigger if exists run_task_done_needs_a_passing_gate on public.run_tasks;
create trigger run_task_done_needs_a_passing_gate
  before insert or update on public.run_tasks
  for each row
  execute function public.run_task_done_needs_a_passing_gate();

comment on function public.run_task_done_needs_a_passing_gate() is
  'G.3: a task reported done while its gate verdict says otherwise is rejected. A NULL verdict is allowed and means the reporting daemon predates gate reports — recorded as unverified, never as passing.';
