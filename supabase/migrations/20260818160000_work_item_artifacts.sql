-- PLANNING ARTIFACTS — a worked HTML example pinned to a work item, plus the markup a human leaves
-- on it. This is an acceptance criterion for the class of work crank provably cannot verify: it has
-- no browser and never sees rendered pixels, so a layout change reads correct to a worker and is
-- wrong (see the render-verify constraint). A drawn-on mockup is the executable check for that class.
--
-- TWO TABLES, not one. work_item_artifacts holds the versioned HTML; work_item_annotations holds the
-- typed markup anchored to a specific version. They're split because annotations OUTLIVE the version
-- they were made on: regenerating an artifact must be able to carry an unresolved complaint forward,
-- which is what `fingerprint` is for.
--
-- Bytes live in the private object store under the `work-item-artifacts` prefix, exactly like
-- work-item-attachments (20260718000000). Nothing here mints a public URL; every read leaves through
-- an authenticated route that has already proven access with an RLS read.

create table public.work_item_artifacts (
  id            uuid primary key default gen_random_uuid(),
  work_item_id  uuid not null references public.work_items(id) on delete cascade,
  org_id        uuid not null references public.orgs(id) on delete cascade,  -- denormalized team wall (mirrors work_items)
  version       integer not null,              -- 1-based, monotonic per work item; never reused
  title         text not null default '',      -- what this version was trying to show
  note          text not null default '',      -- what changed since the previous version (the changelog line)
  storage_path  text not null,                 -- object key within the work-item-artifacts prefix
  size_bytes    bigint not null default 0,
  created_by    uuid references auth.users(id) on delete set null,
  created_at    timestamptz not null default now(),
  -- Set when a human marks this version the agreed one. At most one per work item (partial unique
  -- index below) — accepting a new version must clear the old, which the route does in a transaction.
  accepted_at   timestamptz,
  unique (work_item_id, version)
);

create unique index work_item_artifacts_one_accepted
  on public.work_item_artifacts (work_item_id)
  where accepted_at is not null;

create index work_item_artifacts_item on public.work_item_artifacts (work_item_id, version desc);

-- Typed markup on a specific artifact version.
--
-- `kind` is a CLOSED vocabulary on purpose: it is what makes an annotation convertible into work.
-- scope/behaviour/constraint become acceptance criteria on the work item; question becomes an open
-- question that blocks planning_finalize. A free-text comment field would just recreate the prose
-- problem the artifact exists to solve.
--
-- `anchor` is how the mark finds its target again after a regeneration, and it is deliberately
-- redundant: { selector, rect: {x,y,w,h}, viewport } — the selector survives layout changes, the
-- rect survives selector changes, and whichever still resolves wins. Pixel-only anchoring breaks on
-- every regeneration, which is the failure mode that makes overlay tools useless in practice.
--
-- `shape` is the drawn overlay when there is one: { type, points, color, stroke }, in ARTIFACT space
-- (the iframe is laid out at full content height, so these coordinates do not move with scroll).
-- Null for a plain pin-and-comment.
create table public.work_item_annotations (
  id           uuid primary key default gen_random_uuid(),
  artifact_id  uuid not null references public.work_item_artifacts(id) on delete cascade,
  work_item_id uuid not null references public.work_items(id) on delete cascade,
  org_id       uuid not null references public.orgs(id) on delete cascade,
  author_id    uuid references auth.users(id) on delete set null,
  kind         text not null check (kind in ('scope', 'behaviour', 'constraint', 'question')),
  body         text not null default '',
  anchor       jsonb not null default '{}'::jsonb,
  shape        jsonb,
  -- Stable identity for "the same complaint". Computed by the API from (kind, normalized selector,
  -- viewport class) so a mark that recurs across regenerations is recognizable as unresolved rather
  -- than looking like a brand-new note every version. Not unique — the same spot can carry more than
  -- one kind of remark.
  fingerprint  text not null default '',
  resolved_at  timestamptz,
  created_at   timestamptz not null default now()
);

create index work_item_annotations_artifact on public.work_item_annotations (artifact_id, created_at);
create index work_item_annotations_item on public.work_item_annotations (work_item_id) where resolved_at is null;
create index work_item_annotations_fingerprint on public.work_item_annotations (work_item_id, fingerprint);

alter table public.work_item_artifacts enable row level security;
alter table public.work_item_annotations enable row level security;

-- Read: anyone who can read the PARENT work item — identical wall to work_items' own read policy
-- (org members + the item's creator), joined through work_item_id exactly like work_item_attachments.
-- No authenticated INSERT/UPDATE/DELETE on either table: the API routes authorize with an RLS read
-- and then mutate through the service role, the same posture as work_items/runs/attachments.
create policy "work_item_artifacts: read via work item"
  on public.work_item_artifacts for select to authenticated
  using (exists (select 1 from public.work_items w
                 where w.id = work_item_artifacts.work_item_id
                   and (public.is_org_member(w.org_id) or w.created_by = auth.uid())));

create policy "work_item_annotations: read via work item"
  on public.work_item_annotations for select to authenticated
  using (exists (select 1 from public.work_items w
                 where w.id = work_item_annotations.work_item_id
                   and (public.is_org_member(w.org_id) or w.created_by = auth.uid())));
