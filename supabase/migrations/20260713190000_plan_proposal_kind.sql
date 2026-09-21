-- MCP plan access, part 2: allow the plan_proposal message kind (the agent's promote/archive
-- proposals — a human approves before anything executes). Extends 0048_run_proposal_kind's CHECK.
alter table public.party_messages drop constraint if exists party_messages_kind_check;
alter table public.party_messages add constraint party_messages_kind_check
  check (kind in ('msg', 'status', 'ask', 'system', 'doc', 'run_proposal', 'plan_proposal'));
