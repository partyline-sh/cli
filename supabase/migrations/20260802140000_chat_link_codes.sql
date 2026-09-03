-- Chat transports (docs/epics/chat-transports.md) — account linking.
--
-- A chat platform hands us a user id and nothing else. Turning that into a partyline account needs
-- proof that the same person controls BOTH sides, and the only channel we have to either side is the
-- chat itself. So: partyline (web or CLI, where the caller is already authenticated) mints a
-- short-lived code, the person sends it to the bot, and the bot's webhook — which is the only thing
-- that can see the platform user id — redeems it.
--
-- WHY THIS IS SECURITY-SENSITIVE. Redeeming a code binds a chat account to a partyline account, and
-- from then on that chat account carries the partyline account's ORG PERMISSIONS. A leaked or
-- guessable code is an account takeover, not an inconvenience. Hence: 128 bits of entropy generated
-- server-side, single-use, short TTL, and no listing endpoint.

create table if not exists public.chat_link_codes (
  code       text        primary key,
  user_id    uuid        not null references auth.users(id) on delete cascade,
  -- Bound to ONE platform at mint time. A code minted for Telegram must not be redeemable on
  -- Discord: the two have separate webhook trust models, and a code that works anywhere is a code
  -- that only has to leak once.
  platform   text        not null check (platform in ('slack','telegram','discord','email')),
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  -- Set on redemption rather than deleting the row: a used code must stay REJECTABLE for its whole
  -- TTL. Deleting it makes a replay indistinguishable from a typo, and the two deserve different
  -- answers.
  used_at    timestamptz,
  used_by    text -- the external user id that redeemed it, for audit
);

create index if not exists chat_link_codes_user on public.chat_link_codes (user_id);
create index if not exists chat_link_codes_expiry on public.chat_link_codes (expires_at);

alter table public.chat_link_codes enable row level security;

-- No SELECT policy at all, deliberately. The code is shown ONCE in the response that mints it and is
-- never readable again — not by its owner, not by an org admin. A readable code table is a standing
-- offer to anyone who gets a session token. All access is service-role, from the mint endpoint and
-- the platform webhooks.
