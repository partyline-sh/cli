-- E7.1 (Knowledge Graph L1) — entities: what a fact is ABOUT. Stored as normalized slugs
-- directly on the block (text[] + GIN) — deliberately NOT a table yet: L1 needs tagging,
-- entity-scoped recall, and cross-thread entity pages, all of which RLS on context_blocks
-- already governs. L2 (typed edges) promotes slugs to an entities table when nodes need
-- identity; that migration backfills from this column.

alter table public.context_blocks add column if not exists entities text[] not null default '{}';
create index if not exists context_blocks_entities_gin on public.context_blocks using gin (entities);
