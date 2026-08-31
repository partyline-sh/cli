-- Phase B of projects⊃threads: umbrella projects. A project may have a parent (one level only —
-- enforced in the API): "partyline" (umbrella, often repo-less/source='web') owns child repo-projects
-- web/cli/relay. Thread resolution walks to the ROOT project, so shaping on any child repo plans into
-- the umbrella's thread — multi-repo products get ONE memory and ONE plan.
alter table projects add column if not exists parent_id uuid references projects(id) on delete set null;
create index if not exists projects_parent_id_idx on projects(parent_id) where parent_id is not null;
