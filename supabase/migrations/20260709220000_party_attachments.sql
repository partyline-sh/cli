-- Party FILE ATTACHMENTS — any-type file a human drops into a chat (describe OR a normal party). The web
-- uploads the bytes to a PRIVATE Storage bucket and records a row here; the message that carries the file
-- references the row id(s) in its meta.attachments. When a party agent wakes on a message with
-- attachments, the daemon downloads them into the agent's working dir so it can Read them (the runner
-- half is release-gated). NO type gating — if the agent can't use a type it says so in chat; we never
-- block the upload.

-- Private bucket: no public URLs. Every read/write is mediated by …/parties/[id]/attachments, which
-- streams bytes through the service role after proving access (org membership OR the party-scoped token).
insert into storage.buckets (id, name, public)
values ('party-attachments', 'party-attachments', false)
on conflict (id) do nothing;

create table public.party_attachments (
  id           uuid primary key default gen_random_uuid(),
  party_id     uuid not null references public.parties(id) on delete cascade,
  storage_path text not null,                 -- object key within the party-attachments bucket
  filename     text not null,                 -- original client filename (display + download name)
  content_type text,                          -- client-reported MIME (advisory only; never gates)
  size_bytes   bigint not null default 0,
  created_by   uuid references auth.users(id),
  created_at   timestamptz not null default now()
);
alter table public.party_attachments enable row level security;

-- Read: anyone who can read the PARENT party — identical wall to party_messages (0018) / party_activity,
-- joined through party_id. Writes and byte-streaming go through the service role in the attachments route,
-- authorized by org membership (RLS read of the party) or the party-scoped bearer token (runner download).
create policy "party_attachments: read via party"
  on public.party_attachments for select to authenticated
  using (exists (select 1 from public.parties p
                 where p.id = party_attachments.party_id
                   and (public.is_org_member(p.org_id) or p.created_by = auth.uid())));

create index party_attachments_party on public.party_attachments (party_id, created_at);
