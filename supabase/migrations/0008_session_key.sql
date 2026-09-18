-- partyline 0008_session_key: escrow the session encryption key on the control
-- plane so the web app and notifications can show/share the FULL join link
-- (…/j/<code>#k=<key>&r=<relay>) to everyone authorized to see the session.
--
-- SECURITY TRADEOFF (deliberate, see chat 2026-06-05): previously the key lived
-- ONLY in the host's terminal + the URL fragment and never touched our servers
-- ("we can't read your sessions"). Storing it here means the control plane now
-- holds the key — the relay is still blind (ciphertext only), but partyline COULD
-- decrypt if it tapped the relay. The honest claim is now "encrypted in transit;
-- the relay can't read it", NOT zero-knowledge. Access is gated by the existing
-- sessions RLS: only the host, creator, invitees, and team members can SELECT the
-- row (and therefore the key). Marketing/docs copy updated to match.

alter table public.sessions
  add column if not exists session_key text,   -- base64url Noise key (the #k= fragment)
  add column if not exists relay_addr  text;   -- host:port the joiner dials (the &r= fragment)
