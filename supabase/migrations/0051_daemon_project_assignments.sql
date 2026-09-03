-- WEB-ASSIGNABLE PROJECTS (web side). The owner assigns a project to one of their machines from the
-- web by picking a directory the DAEMON itself advertised (a candidate handle) and giving it a label.
-- We queue the intent here; the daemon's outbound stream drains it and emits an `assign_project`
-- event, which the daemon resolves (handle → local abs path it advertised) and binds — reference-not-
-- command intact: the server only ever sends a LABEL + an opaque HANDLE, never a path.
create table public.daemon_project_assignments (
  id           uuid primary key default gen_random_uuid(),
  daemon_id    uuid not null references public.daemons(id) on delete cascade,
  handle       text not null,  -- opaque candidate handle the daemon advertised (hash of a local path)
  label        text not null,
  preset       text,           -- 'spec' | 'chat' | null
  engine       text,
  requested_by uuid,
  created_at   timestamptz not null default now()
);
alter table public.daemon_project_assignments enable row level security;
-- No authenticated policies: the owner-gated route inserts service-role, and the daemon stream
-- drains service-role. Users never read/write this table directly.
create index daemon_project_assignments_daemon on public.daemon_project_assignments (daemon_id);
