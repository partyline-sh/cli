-- partyline 0007: account linking support + clean user deletion.
--
-- (a) notify_email: where a user wants notifications, decoupled from their login
--     identities (they may log in via email-OTP / Google / GitHub with different
--     emails). Falls back to the primary auth email when null.
-- (b) FK hardening: several refs to auth.users were created without on-delete
--     behavior, so deleting (or merging) a user hit FK walls (e.g. device_codes).
--     Repoint them to cascade (ephemeral rows) or set null (rows others rely on).
--     profiles.id / org_members.user_id / team_members.user_id / api_tokens.user_id
--     already cascade — left as-is.
--
-- Applied BY HUMAN per CLAUDE.md hard rule. If a constraint name differs in your
-- DB, the matching statement will error — paste it and we'll adjust.

-- (a) notification email
alter table public.profiles add column if not exists notify_email text;

-- (b) auth.users FK behavior

-- orgs.created_by: keep the org if its creator is deleted (others may be members)
alter table public.orgs alter column created_by drop not null;
alter table public.orgs drop constraint orgs_created_by_fkey;
alter table public.orgs add constraint orgs_created_by_fkey
  foreign key (created_by) references auth.users(id) on delete set null;

-- sessions: host's sessions are ephemeral → cascade; created_by → set null
alter table public.sessions drop constraint sessions_host_user_fkey;
alter table public.sessions add constraint sessions_host_user_fkey
  foreign key (host_user) references auth.users(id) on delete cascade;
alter table public.sessions drop constraint sessions_created_by_fkey;
alter table public.sessions add constraint sessions_created_by_fkey
  foreign key (created_by) references auth.users(id) on delete set null;

-- device login codes are throwaway → cascade
alter table public.device_codes drop constraint device_codes_user_id_fkey;
alter table public.device_codes add constraint device_codes_user_id_fkey
  foreign key (user_id) references auth.users(id) on delete cascade;

-- teams.created_by → set null (team survives)
alter table public.teams alter column created_by drop not null;
alter table public.teams drop constraint teams_created_by_fkey;
alter table public.teams add constraint teams_created_by_fkey
  foreign key (created_by) references auth.users(id) on delete set null;

-- org invites: keep the row, drop the user link
alter table public.org_invites alter column created_by drop not null;
alter table public.org_invites drop constraint org_invites_created_by_fkey;
alter table public.org_invites add constraint org_invites_created_by_fkey
  foreign key (created_by) references auth.users(id) on delete set null;
alter table public.org_invites drop constraint org_invites_accepted_by_fkey;
alter table public.org_invites add constraint org_invites_accepted_by_fkey
  foreign key (accepted_by) references auth.users(id) on delete set null;

-- session invites: null the user link (also cascades when the session is deleted)
alter table public.session_invites drop constraint session_invites_user_id_fkey;
alter table public.session_invites add constraint session_invites_user_id_fkey
  foreign key (user_id) references auth.users(id) on delete set null;
