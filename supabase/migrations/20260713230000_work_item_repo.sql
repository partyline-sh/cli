-- Phase B3: task-level repo targeting. In an umbrella project (multi-repo), each TASK names the child
-- repo (project label) it builds in — assigned by the shaping agent in the plan block, carried here,
-- and used to default the daemon/label when the task is promoted or started.
alter table work_items add column if not exists repo_label text;
