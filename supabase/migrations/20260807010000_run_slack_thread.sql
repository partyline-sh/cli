-- #823 thread 3 — one Slack conversation per incident. The FIRST Slack message about a run opens a
-- thread; its `ts` is remembered here, and every later alert for the same run replies inside it.
-- A crash-looping deploy stops double-pinging the channel on every cycle: one root message, the
-- rest is a thread you open when you care. A channel people mute is worse than no channel.
alter table public.runs add column if not exists slack_ts text;
