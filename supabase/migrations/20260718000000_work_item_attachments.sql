-- Work-item FILE ATTACHMENTS — durable any-type files pinned to a planning node (epic/feature/task),
-- so requirements docs, screenshots, specs etc. survive with the work item instead of living only in a
-- party chat. Mirrors party_attachments (20260709220000): a PRIVATE Storage bucket holds the bytes, a
-- row here holds the metadata, and every byte read/write is mediated by the
-- /api/v1/work-items/[id]/attachments routes through the service role after an RLS access proof.
-- NO type gating — extraction (extracted_text) is a later, separate concern; we never block an upload
-- by type. Size (25MB) and per-item count (20) caps are enforced in the API layer.

-- Private bucket: no public URLs, no anon/authenticated storage.objects policies — the service role is
-- the only reader/writer, same posture as party-attachments. file_size_limit is defense in depth behind
-- the route's own cap.
insert into storage.buckets (id, name, public, file_size_limit)
values ('work-item-attachments', 'work-item-attachments', false, 26214400)
on conflict (id) do nothing;

create table public.work_item_attachments (
  id             uuid primary key default gen_random_uuid(),
  work_item_id   uuid not null references public.work_items(id) on delete cascade,
  org_id         uuid not null references public.orgs(id) on delete cascade,  -- denormalized team wall (mirrors work_items)
  uploaded_by    uuid references auth.users(id) on delete set null,
  filename       text not null,                 -- original client filename (display + download name)
  mime_type      text,                          -- client-reported MIME (advisory only; never gates)
  size_bytes     bigint not null default 0,
  storage_path   text not null,                 -- object key within the work-item-attachments bucket
  extracted_text text,                          -- filled by a later extraction task; null until then
  created_at     timestamptz not null default now()
);
alter table public.work_item_attachments enable row level security;

-- Read: anyone who can read the PARENT work item — identical wall to work_items' own read policy
-- (org members + the item's creator), joined through work_item_id exactly like party_attachments
-- joins through party_id. No authenticated INSERT/UPDATE/DELETE: the API routes authorize (an RLS
-- read proves membership) then mutate with the service role, same posture as work_items/runs.
create policy "work_item_attachments: read via work item"
  on public.work_item_attachments for select to authenticated
  using (exists (select 1 from public.work_items w
                 where w.id = work_item_attachments.work_item_id
                   and (public.is_org_member(w.org_id) or w.created_by = auth.uid())));

create index work_item_attachments_item on public.work_item_attachments (work_item_id, created_at);
