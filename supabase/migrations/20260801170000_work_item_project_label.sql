-- Work items carry a target project (#839, epic #836).
--
-- A work item lives on a THREAD, which says nothing about where its code should be built. To Start
-- one you have always had to pick a machine and a project on the board — fine for an item a human
-- shaped there, and pure friction for one an agent filed from inside the repo it is about, where the
-- answer was never in doubt.
--
-- It is also load-bearing for the readiness gate (#837): "does this item name a project that
-- actually exists" is one of the four dimensions, and it is exactly the kind of check a language
-- model cannot talk its way past — the label resolves or it does not. Without somewhere to record
-- the label, that dimension could only ever fail.
--
-- NULLABLE, deliberately. Every existing row keeps working exactly as before: no label means the
-- board asks, which is today's behaviour. Additive and backward-compatible, so the old app runs
-- against the new schema during the swap window.

alter table work_items add column if not exists project_label text;

comment on column work_items.project_label is
  'Optional partyline project label this item builds in. Set when the filer knows it (an agent '
  'filing from inside a repo); NULL means the board asks at Start, as it always has. A REFERENCE, '
  'not a path — the daemon resolves a label to a local directory through its own registry, and no '
  'value here ever becomes a filesystem path or an argv entry.';
