-- ASSIGNMENT STATE MACHINE (web-assignable projects, slice 2).
--
-- SEQUENCING: this migration MUST land on main (and be applied) BEFORE the code that reads these
-- columns — per the repo rule, it belongs in its OWN PR, separate from the route changes that ship
-- alongside it in this branch. Split it out at review time: it is inert on its own (additive
-- columns with defaults; the running app ignores them), and applying it early costs nothing, while
-- shipping the code first makes every assignment write fail on a box that hasn't got the columns.
--
-- 0051 queued a bind intent and the stream DELETED the row as it drained it, so an assignment had
-- no life after delivery — there was nowhere for the machine to say "cloning", "ready", or "failed".
-- Clone-on-demand needs exactly that: the web picks a project + a DESTINATION the machine
-- advertised, the machine clones `repo_url` into it and reports progress back.
--
-- Additive and backward-compatible, per the migration contract: every new column is nullable or
-- defaulted, so the OLD app runs unchanged against this schema during the swap window (it simply
-- ignores state and still deletes rows it has drained).
alter table public.daemon_project_assignments
  add column if not exists destination_handle text,   -- opaque candidate handle: WHERE the clone lands
  add column if not exists repo_url            text,  -- the project's git URL — DATA, never a command
  add column if not exists state               text not null default 'queued',
  add column if not exists reason              text,  -- why it failed (or a note on any transition)
  add column if not exists delivered_at        timestamptz, -- stamped when the stream pushed it
  add column if not exists updated_at          timestamptz not null default now();

-- `handle` named an EXISTING repo the machine advertised. A clone-on-demand assignment has no such
-- repo yet — it carries a destination_handle instead — so the column becomes optional. The route
-- enforces "one of the two", which is the rule that actually matters.
alter table public.daemon_project_assignments alter column handle drop not null;

do $$ begin
  alter table public.daemon_project_assignments
    add constraint daemon_project_assignments_state_check
    check (state in ('queued', 'cloning', 'registering', 'ready', 'failed'));
exception when duplicate_object then null; end $$;

-- The stream now drains by "not yet delivered" instead of by deleting.
create index if not exists daemon_project_assignments_undelivered
  on public.daemon_project_assignments (daemon_id)
  where delivered_at is null;
