-- The import upsert never worked. Every call returned:
--   "there is no unique or exclusion constraint matching the ON CONFLICT specification"
--
-- ON CONFLICT cannot target a PARTIAL unique index unless the statement repeats the index's WHERE
-- predicate — and PostgREST's onConflict parameter has no way to express one. So the index existed,
-- looked right, and could never be used by the only code path that needed it.
--
-- The partial predicate was a mistake on its own terms. It was there so hand-created items (which
-- have no source) would stay unconstrained — but Postgres already treats NULLs as DISTINCT in a
-- unique index, so any number of rows with a null source_tool coexist happily. The predicate bought
-- nothing and cost the entire feature.
--
-- This is the claim the whole "import your backlog" story rests on: re-importing a roadmap must
-- UPDATE matching items, never duplicate them. It was verified by reading the index definition and
-- shipped without ever being exercised. The first real import found it immediately.
drop index if exists work_items_source_unique;

create unique index if not exists work_items_source_unique
  on public.work_items (org_id, source_tool, source_id);
