# Security

## Reporting a vulnerability

Email **security@partyline.sh** with "security" in the subject. Please do not open a public issue for a
vulnerability — a public report is a disclosure, and it starts the clock before there is a fix.

Include what you need to make the problem reproducible: what you did, what happened, and what you
expected. A proof of concept helps enormously. If you are unsure whether something counts, report it
— a false alarm costs an email, and the alternative costs more.

You will get an acknowledgement within **3 working days**. If you do not, assume the mail went
astray and try again rather than assuming it was ignored.

This is a small project without a bug bounty. What we can offer is a fast reply, credit in the fix
if you want it, and honesty about the timeline.

## What matters most here

partyline runs AI coding agents, and the loops that review their work, on machines its users own.
The highest-severity issues are the ones that let an agent do what only the person or partyline
may do:

- **A session or builder deciding for itself.** A loop's decisions are made by partyline's decider
  (the default) or by the person in the approval popup, behind a four-digit code shown on screen.
  Loop state lives under `~/.partyline/loops/`, outside the repository, and builders cannot write
  it. A path that lets the session under review, or a builder, approve, rule on a dispute or forge
  a decision is the most serious class of bug in this codebase.
- **partyline committing, pushing or rewriting the user's repository.** The one exception is the
  commit partyline makes for a batch step that passed review, in the batch's checkout. A builder
  cannot push or merge. Anything that pushes, or commits outside that exception, is critical.
- **Anything that weakens a check or review silently.** A check or review that reports "passed"
  when it did not run is worse than none, because it is trusted.
- **Text typed into a session the person is using.** The chat and loop wakes type only into an
  idle, unwatched pane with an empty input box.
- **Session and relay confidentiality.** Pane and chat content sent through the relay is
  end-to-end encrypted, and the relay is meant to be blind to it.
- **A public self-hosted relay.** The Go relay accepts external connections only through
  trusted HTTPS (its plain HTTP listener is loopback-only). A reverse proxy must overwrite
  or append the actual client address in X-Forwarded-For, and only the immediate trusted
  proxy's rightmost hop is used. Trusting arbitrary forwarded headers can defeat per-client
  limits; omitting the header behind a trusted proxy puts callers in a shared bucket.
  HTTP rates, unauthenticated connections, pending joins and per-peer queues are bounded.
  Those bounds limit resource use; they are not protection against every denial of service.
- **Private gateway membership.** Discovery advertises a friendly name and HTTPS origin,
  not credentials. A name is not authorization. A join requires the owner to approve the
  six-digit comparison and the joining computer to confirm it. The Noise XX v2 handshake
  commits to the initiator's static key, nonce and exact request before the gateway response,
  then checks the reveal before exposing a pending join. This prevents final-message code
  grinding. Approval endpoints are owner-only and the resulting token stays inside that
  encrypted channel. Membership does not pair or authorize a phone on any computer.
- **Gateway credentials and services.** The gateway data directory is 0700; its configuration,
  owner token, identity key and SQLite registry are private files (0600). A server-only owner
  credential is separate from this computer's existing relay client. Setup reviews changes
  before installing a macOS LaunchAgent or Linux systemd user unit. These run as their owner,
  not an isolated user: other programs running as that user remain within the trust boundary.
  Losing a private gateway host or its HTTPS/VPN route interrupts all routes through it.
- **The phone pairing QR is a credential.** Its short-lived secret is part of the
  pairing authorization. Keep the QR and full pairing link private. The four-digit phone
  code is derived from the phone public key; it is not an independent secret if someone
  has obtained that QR. Compare the full fingerprint too. If an offer leaks, refuse it
  and use a fresh one; revoke an authorization already given to an unintended phone.
- **Browser code versus native code.** A compromised relay can serve modified browser-client
  code and steal that client's keys or content. Asset hashes detect changes against a saved
  deployment baseline but cannot stop a relay from serving different bytes to different
  clients. The native UI is bundled in the installed app, not loaded from the relay; trust
  in that app and its operating system still matters.
- **Files a phone writes to the computer.** A paired phone may upload a photo or file. It is
  written only under `~/Library/Caches/sh.partyline/uploads/<phone>/` (or the XDG cache on
  Linux), under a name the computer chooses, never executable, within per-file and per-phone
  limits, and kept only if its size and SHA-256 match. A path that lets a phone choose where a file
  lands, overwrite another file, or upload while unpaired or revoked is serious.
  Limits are 20 MiB per file and 200 MiB cached per phone, with oldest finished files evicted
  when room is needed. A confirmed upload adds a quoted path to a draft and never sends it.

## Out of scope

- Reports from automated scanners with no demonstrated impact.
- Missing hardening headers on the documentation site with no exploit path.
- Anything requiring an attacker to already control the user's machine or their engine credentials:
  partyline and the agents it starts run as their owner, by design.

## Handling

Fixes go through the same CI and signed release as every other change, because a rushed security
fix that breaks the release helps nobody. If a fix must skip a step, the release notes say so.
