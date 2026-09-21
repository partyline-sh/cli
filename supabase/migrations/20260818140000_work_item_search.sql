-- Work-item similarity search — the substrate for "search before you decompose".
--
-- The problem: the Describe agent has plan_read (this conversation's own tree) and backlog_read
-- (runs), but NO way to ask "has anyone already planned this?". So it happily re-specifies work that
-- already exists as an epic/feature/task somewhere in the org, and a human only notices at the board.
--
-- What this adds, all additive and inert until something calls it:
--   • a generated tsvector over title (weight A) + document (weight B), with a GIN index — the
--     full-text half, good at "attachment upload for work items".
--   • pg_trgm + a trigram index on title — the fuzzy half, which is what actually saves a query
--     that is two words long or misspelled ("attachements"), where to_tsquery matches nothing.
--   • search_work_items(), a SECURITY DEFINER RPC. The caller (the party-token work-search route)
--     proves the org from the PARTY ROW and passes it in. RLS on work_items is untouched — this is
--     the same posture as every other agent-facing read: authorize at the route, query with the
--     service role.
--
-- THE ORG ARGUMENT IS THE WHOLE RISK, so it is fenced twice. A SECURITY DEFINER function that takes
-- an org id and bypasses RLS is a cross-team read for anyone who can call it — and PostgREST
-- publishes every executable public function at /rpc/<name>, so a grant to `authenticated` would
-- have let ANY signed-in user pass ANY org's uuid and read that team's planning titles and spec
-- snippets. Hence:
--   1. EXECUTE is granted to service_role ONLY. The one caller is the work-search route, which runs
--      with the service role after proving the party token. anon/authenticated cannot reach it.
--   2. The body ALSO checks membership whenever there IS an end user on the request
--      (auth.uid() is not null → public.is_org_member(p_org) must hold). Under the service role
--      there are no JWT claims, so auth.uid() is null and the route passes. This is belt and braces
--      for the day someone adds a grant without re-reading this file: the worst case then is an
--      empty result for a non-member, not another team's backlog.

-- pg_trgm is contrib, present in the postgres:16 image, and apply-migrations.sh runs psql as the
-- superuser — so this is not guarded the way 0058 guards pg_cron. It is a HARD dependency here
-- (similarity() is half the ranking); failing the deploy loudly beats a function that errors on
-- every call afterwards.
create extension if not exists pg_trgm;

alter table public.work_items
  add column if not exists search_tsv tsvector
  generated always as (
    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(document, '')), 'B')
  ) stored;

create index if not exists work_items_search_tsv on public.work_items using gin (search_tsv);
create index if not exists work_items_title_trgm on public.work_items using gin (title gin_trgm_ops);

-- p_exclude_party drops items that were FILED FROM one particular party — the calling describe
-- session's own already-recorded tree. Without it, a session that finalized once and kept talking
-- would be told it is a duplicate of itself.
--
-- Archived items never match: they are decisions to NOT do the work, so surfacing one as "this
-- already exists" would be actively misleading.
create or replace function public.search_work_items(
  p_org           uuid,
  p_query         text,
  p_limit         int  default 8,
  p_exclude_party uuid default null
)
returns table (
  id        uuid,
  kind      text,
  status    text,
  title     text,
  thread_id uuid,
  readiness int,
  snippet   text,
  rank      real
)
language sql
security definer
stable
set search_path = public
as $$
  with q as (
    -- websearch_to_tsquery never raises on user text (plainto_/to_tsquery can); an unparseable
    -- query simply yields an empty tsquery, and the trigram arm still answers.
    select websearch_to_tsquery('english', coalesce(p_query, '')) as tsq,
           nullif(btrim(coalesce(p_query, '')), '')               as raw
  )
  select w.id,
         w.kind,
         w.status,
         w.title,
         w.thread_id,
         w.readiness,
         ts_headline(
           'english',
           left(coalesce(nullif(w.document, ''), w.title), 4000),
           q.tsq,
           'StartSel=<<, StopSel=>>, MaxFragments=1, MaxWords=30, MinWords=8'
         ) as snippet,
         greatest(ts_rank(w.search_tsv, q.tsq), similarity(w.title, q.raw))::real as rank
  from public.work_items w, q
  where q.raw is not null
    -- Layer 2 of the org fence (see the header): a real end user may only search their OWN org.
    -- Scalar, evaluated once, and null under the service role — which is how the route gets through.
    and (auth.uid() is null or public.is_org_member(p_org))
    and w.org_id = p_org
    and w.status <> 'archived'
    and (p_exclude_party is null or w.origin_party_id is distinct from p_exclude_party)
    and (w.search_tsv @@ q.tsq or similarity(w.title, q.raw) > 0.3)
  order by rank desc, w.updated_at desc
  -- Clamped in the DB as well as the route: a caller asking for 10 000 rows gets 25.
  limit greatest(1, least(coalesce(p_limit, 8), 25));
$$;

-- `revoke ... from public` also strips the implicit PUBLIC execute grant every new function gets;
-- anon and authenticated are named as well so the intent survives a future `grant ... to public`
-- being copied in from a neighbouring migration. service_role is the ONLY caller.
revoke all on function public.search_work_items(uuid, text, int, uuid) from public, anon, authenticated;
grant execute on function public.search_work_items(uuid, text, int, uuid) to service_role;
