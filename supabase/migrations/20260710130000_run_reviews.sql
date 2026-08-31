-- Review agent (advisory) — the optional review a human requests on a finished run sitting in the
-- board's Review column. The reviewer runs on the ORIGINAL daemon (it has the branch), diffs the
-- run's work against its task, and posts back a code review: a quality GRADE (A–F), a summary, and an
-- issues list. Advisory only — the grade is a signal next to Accept, it never blocks it.
--
-- Same INVARIANT as runs/run_tasks: the control plane only holds DATA, and only the OWNING daemon
-- (service role, device token) writes these rows — team members read via the parent run's wall.

create table public.run_reviews (
  id             uuid primary key default gen_random_uuid(),
  run_id         uuid not null references public.runs(id) on delete cascade,  -- the TARGET run reviewed
  grade          text not null check (grade in ('A', 'B', 'C', 'D', 'F')),
  summary        text,
  issues         jsonb not null default '[]'::jsonb,   -- [{severity:'high'|'med'|'low', text}]
  reviewer_model text,                                 -- which engine/model produced it (provenance)
  created_at     timestamptz not null default now()
);
alter table public.run_reviews enable row level security;

-- Read: anyone who can read the PARENT (target) run — the same wall as run_tasks (0037), joined
-- through run_id. The latest row per run_id is the current review.
create policy "run_reviews: readable via parent run"
  on public.run_reviews for select to authenticated
  using (exists (
    select 1 from public.runs r
    where r.id = run_reviews.run_id
      and (public.is_org_member(r.org_id) or r.created_by = auth.uid())
  ));

-- No authenticated INSERT/UPDATE policy: the reviewing daemon (device token) inserts via the service
-- role through …/daemon/run/[id]/review-result, which authorizes org membership first. Read-only here.

create index run_reviews_run on public.run_reviews (run_id, created_at desc);

-- A hidden `review` run points at the run it reviews. Nullable (only review runs set it); cascades so
-- a review run is cleaned up if its target is deleted.
alter table public.runs add column if not exists review_of uuid references public.runs(id) on delete cascade;
create index if not exists runs_review_of on public.runs (review_of);

-- REALTIME: the run detail page subscribes so a review appears without a refresh (RLS still authorizes
-- each subscriber per row via the policy above). Guarded so a re-run / missing publication is a no-op.
do $$
begin
  alter publication supabase_realtime add table public.run_reviews;
exception when undefined_object then null; when duplicate_object then null;
end $$;
