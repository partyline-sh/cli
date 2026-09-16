-- MCP plan access, part 1: attribution. Every agent write to a work item is stamped so the tree can
-- show WHO touched it (the ✎ agent marker) — a self-managing board must stay auditable. Set by the
-- party-token plan endpoints only; human edits (cookie auth) leave these untouched.
alter table work_items add column if not exists agent_touched_at timestamptz;
alter table work_items add column if not exists agent_party_id uuid references parties(id) on delete set null;
