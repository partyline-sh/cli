-- partyline 0038_perf: add missing indexes for the two hot N+1 / seq-scan paths (#14, #15).

-- #15: "list my orgs" filters org_members by user_id, but the PK is (org_id, user_id)
-- so a user_id-only filter can't use it. Hit on the sidebar + /teams on every load.
create index if not exists org_members_user_id_idx on public.org_members(user_id);

-- #14: session queries filter by status (active vs ended); without an index this is a
-- seq scan on every dashboard / reaper pass. Cover the bare-status lookups.
create index if not exists sessions_status_idx on public.sessions(status);
