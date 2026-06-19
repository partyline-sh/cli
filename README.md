# partyline

**Your terminal, multiplayer.** One person starts a shared shell on their machine;
teammates join from any terminal on any OS with one command. Everyone sees the same
live terminal; the host grants typing to whoever should drive. Run anything in it —
including an AI coding CLI (`claude`, `codex`, `aider`) — and the whole team can watch
and steer it together. Sessions are encrypted; the relay that connects you is blind
(it only forwards ciphertext).

```
host:    ptln
         ── session up. share the join link printed in your terminal.

joiner:  ptln join <link>        (or plain ssh on a LAN)
```

## Architecture

```
HOST MACHINE
┌──────────────────────────────────────────────────┐
│ partyline (single Go binary)                     │
│  ├─ ptysess: your real $SHELL on a pty,          │
│  │   raw byte-passthrough, mirrored to joiners   │
│  ├─ view-only guests + host-grantable typing     │
│  │   (full-access drives; viewers watch)         │
│  └─ control via ctrl-\ prefix + typed /p commands│
└──────────────────────────────────────────────────┘
   joiners connect via:
     • relay (pppp.sh) — the universal, code-auth path
     • plain ssh on a LAN
   end-to-end Noise (NNpsk0) channel; the relay only sees ciphertext.
```

Key decisions:
- **Sessions are the product.** A session is one shared terminal. Want a shared AI
  session? Start a session and run your agent CLI in it — no special mode.
- **Encrypted, relay-blind.** The session key rides in the join link's `#` fragment;
  the relay forwards ciphertext and never holds the key. (The control plane escrows
  the key to power web/invite links — so this is "encrypted, we don't read it," not
  zero-knowledge. See `docs/reviews/0004`.)
- **Identity-first joins.** Joiners present a control-plane-signed identity assertion;
  the host knows who's on the line. Viewers can never drive unless granted full access.

## Control plane
Web app + `/api/v1` on CPLN; Supabase Postgres with RLS as the authorization boundary;
relay pool for non-LAN joiners. See `docs/SYSTEM-DESIGN.md`.

## Install

```sh
# macOS (Homebrew)
brew install partyline/tap/partyline

# Linux / macOS (script)
curl -fsSL https://partyline.sh/install.sh | sh
```

`partyline` installs as both `partyline` and the short alias `ptln`.

## License

[MIT](LICENSE). This is the open-source partyline client (CLI + blind relay), mirrored
from the partyline monorepo. The web control plane is a separate, hosted service.
