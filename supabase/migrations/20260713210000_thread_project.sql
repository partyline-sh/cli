-- Phase A of projects⊃threads: a context thread belongs to a project (nullable while existing
-- threads await attachment; new threads are created attached). Plans stay thread-anchored — this
-- adds the association layer so Shape needs ONE picker and the project page owns its context.
alter table threads add column if not exists project_id uuid references projects(id) on delete set null;
create index if not exists threads_project_id_idx on threads(project_id) where project_id is not null;
