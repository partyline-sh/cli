-- PROJECTS · rename cascade (Phase 2). `label` is the JOIN KEY across projects / runs /
-- daemon_projects, so renaming it must cascade. The web updates projects.label + runs.project_label
-- (org-scoped) directly, but a machine's advertised label lives in the daemon's OWN local registry
-- and is REPLACED on every heartbeat/mirror — so a server-side rename of daemon_projects would just
-- revert. Instead we queue a relabel here; the daemon's stream drains it, renames its local registry
-- entry old→new, and re-mirrors. Reference-not-command intact: only two LABEL strings cross the wire.
create table public.daemon_relabels (
  id           uuid primary key default gen_random_uuid(),
  daemon_id    uuid not null references public.daemons(id) on delete cascade,
  old_label    text not null,
  new_label    text not null,
  requested_by uuid,
  created_at   timestamptz not null default now()
);
alter table public.daemon_relabels enable row level security;
-- No authenticated policies: the rename cascade inserts service-role; the daemon stream drains
-- service-role. Users never read/write this directly.
create index daemon_relabels_daemon on public.daemon_relabels (daemon_id);
