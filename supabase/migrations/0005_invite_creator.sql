-- 0005: planned sessions are created by `created_by` before any host claims
-- them, so the host-only invite policies from 0001 lock the creator out of
-- pre-arming invites. Widen invite insert/read to (host OR creator).

drop policy if exists "session_invites: host insert" on public.session_invites;
create policy "session_invites: host or creator insert"
  on public.session_invites for insert to authenticated
  with check (exists (
    select 1 from public.sessions s
    where s.id = session_id
      and (s.host_user = auth.uid() or s.created_by = auth.uid())
  ));

drop policy if exists "session_invites: host or invitee read" on public.session_invites;
create policy "session_invites: host, creator, or invitee read"
  on public.session_invites for select to authenticated
  using (
    user_id = auth.uid()
    or lower(email) = lower(coalesce(auth.jwt()->>'email',''))
    or exists (
      select 1 from public.sessions s
      where s.id = session_id
        and (s.host_user = auth.uid() or s.created_by = auth.uid())
    )
  );
