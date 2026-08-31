-- partyline 0013_slack_app: store the channel-bound incoming webhook from the
-- Slack OAuth install (#8/#13). The bot token + team already live on slack_installs
-- (0004); these columns capture WHERE notifications go — the single channel the
-- installer picked during OAuth (Slack's incoming-webhook grant returns the URL
-- + channel). Writes are service-role only (OAuth callback); reads stay RLS-gated
-- to org owners/admins (policy from 0004 is unchanged).

alter table public.slack_installs
  add column if not exists webhook_url        text,
  add column if not exists slack_channel_id   text,
  add column if not exists slack_channel_name text;

-- The /partyline slash command resolves a Slack workspace (team_id) -> installs.
-- Look-ups are by slack_team_id, which is not the PK (org_id is), so index it.
create index if not exists slack_installs_team on public.slack_installs (slack_team_id);
