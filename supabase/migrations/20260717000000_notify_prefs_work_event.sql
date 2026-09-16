-- The Notifications settings UI has a "My work run finishes or needs me" row with event_type 'work',
-- but the notify_prefs check constraint from 0004 only allowed session_invite/team_session/mention/
-- digest. Saving prefs therefore failed with:
--   new row for relation "notify_prefs" violates check constraint "notify_prefs_event_type_check"
-- and because the endpoint upserts all rows in ONE batch, the whole save aborted — so turning email
-- OFF never persisted, and run emails kept sending against the default. Add 'work' to the allowed set.
alter table public.notify_prefs drop constraint if exists notify_prefs_event_type_check;
alter table public.notify_prefs add constraint notify_prefs_event_type_check
  check (event_type in ('work', 'session_invite', 'team_session', 'mention', 'digest'));
