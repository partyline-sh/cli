```
'########:::::'###::::'########::'########:'##:::'##:'##:::::::'####:'##::: ##:'########:
 ##.... ##:::'## ##::: ##.... ##:... ##..::. ##:'##:: ##:::::::. ##:: ###:: ##: ##.....::
 ##:::: ##::'##:. ##:: ##:::: ##:::: ##:::::. ####::: ##:::::::: ##:: ####: ##: ##:::::::
 ########::'##:::. ##: ########::::: ##::::::. ##:::: ##:::::::: ##:: ## ## ##: ######:::
 ##.....::: #########: ##.. ##:::::: ##::::::: ##:::: ##:::::::: ##:: ##. ####: ##...::::
 ##:::::::: ##.... ##: ##::. ##::::: ##::::::: ##:::: ##:::::::: ##:: ##:. ###: ##:::::::
 ##:::::::: ##:::: ##: ##:::. ##:::: ##::::::: ##:::: ########:'####: ##::. ##: ########:
..:::::::::..:::::..::..:::::..:::::..::::::::..:::::........::....::..::::..::........::
```

**One terminal for every AI coding session — and a way to share any of them, live.**

`partyline` (`ptln`) is a single Go binary that does two things:

1. **`ptln` — your AI session manager** (the front door; `ptln llms` is the same thing).
   Find, resume, and run every Claude Code / Codex / Gemini / `llm` session on your machine
   from one launcher — and host several at once in a single multiplexed terminal instead of
   a tab per agent.
2. **`ptln start` — share any session, live.** Hand a teammate a link and they watch your
   terminal in real time over an end-to-end-encrypted, blind relay — pair on a bug, review an
   agent's work, run an incident.

Local-first: no daemon, no network, nothing leaves your machine unless you choose to share.

## `ptln llms` — the AI session manager

Every AI CLI scatters its sessions across its own on-disk store. `ptln llms` reads them all
and puts them in one interactive launcher:

```sh
ptln llms                 # browse every session (claude · codex · gemini · llm)
ptln llms <id> [<id>…]    # open one or several straight into a multiplexed terminal
ptln llms --resume        # reopen the exact set you had open last time
ptln --resume             # same — the front door's flags work at the top level too
```

Inside the launcher:

- **Browse + search** every session with rich detail — project, git branch, model, tokens,
  first prompt, recent messages, MCP servers, skills. Sort by **last used**; live sessions
  are flagged ⏳ waiting-for-you vs ● running.
- **Run several in one terminal.** Open sessions as windows in one multiplexer and switch
  from the tab bar — **`ctrl-\ ←/→`** (⏎ to commit) or **`ctrl-\ 1`–`9`** to jump. No more tab-per-agent.
- **Resume with the right permissions** — a quick picker (default / accept-edits / plan /
  bypass) before the agent picks up where it left off.
- **Share it** (`S`) — host the session over the relay so a teammate can watch live.
- Open a plain **shell** (`n`), **pin** / **rename** / **diff** a session, **refresh**, and
  switch **color themes** (`t` — dark, light, and a few in between).

```
↑↓ move · ⏎ open · space multi-select · S share · n terminal · / search
 s sort · r rename · d diff · t theme · R refresh · ? all keys
```

## Shared sessions — your terminal, multiplayer

Start a shared terminal anyone can join and watch (and drive, if you grant it):

```
host:    ptln start                 # session up — share the printed join link
joiner:  ptln join '<link>'         # or plain ssh on a LAN
```

Everyone sees the same live terminal; the host grants typing to whoever should drive. Run
anything in it — a shell, a build, an AI agent — and the team watches together. Encrypted
end-to-end; the relay only forwards ciphertext.

## Parties — humans + AI agents on one channel

```sh
ptln party '<link>' --name dev      # bring an agent into a shared humans+agents channel
```

Address people and agents by name (`@name` / `@all` / `@any`); the runner wakes an agent
only when it's addressed. Parties share a working doc agents propose edits to and humans
approve. (Hosted from the web/Slack; see the docs.)

**Remote launch (early access).** Instead of running `ptln party` by hand, a teammate can
click **Add agent** on the party page and start a grounded agent on *your* machine, in the
right project dir — after you opt in (`ptln daemon enable` + `add-project`) and approve each
launch in `ptln daemon run`. The control plane only ever sends a project *label*, never a
path or command (reference-not-command); the agent runs with read-only tools. See the docs.

## Architecture

```
                       YOUR MACHINE  (local-first, no daemon)

   ptln llms  ── AI session manager + multiplexer
   ┌──────────────────────────────────────────────────────────────┐
   │ reads each tool's own on-disk session store:                  │
   │   ~/.claude · ~/.codex · ~/.gemini · llm                      │
   │                                                               │
   │ opens the ones you pick as children — one full-screen at a    │
   │ time ("windows"), switched with ctrl-\ ←/→ or 1–9:            │
   │                                                               │
   │    [ claude ]   [ codex ]   [ gemini ]   [ shell ]            │
   │       each = a real process on a PTY → vt emulator            │
   │       switch repaints the target's Snapshot()                 │
   │       a gate lets ONLY the active child write your screen     │
   └───────────────────────────────┬───────────────────────────────┘
                                    │  share a session  (press  S )
                                    ▼
   ptysess  ── one terminal, many viewers
   ┌──────────────────────────────────────────────────────────────┐
   │ host drives; guests are view-only until granted typing        │
   │ end-to-end Noise NNpsk0  (X25519 / ChaCha20-Poly1305)         │
   └───────────────────────────────┬───────────────────────────────┘
                                    │  ciphertext only
                       blind relay (pppp.sh) ── forwards bytes, holds no key
                                    │
                      joiners:  ptln join '<link>'   (or ssh on a LAN)

   control plane (hosted, separate from this repo): web + /api/v1 + Supabase
   — accounts, join links, presence.  Never sees your terminal bytes.
```

How it fits together:

- **`ptln llms` is entirely local.** It parses each tool's own session files (no daemon, no
  network) and runs the sessions you open as child processes, each on its own PTY driven
  through a VT emulator. Switching repaints the target child's `Snapshot()`; only the active
  child's output reaches your screen.
- **Sharing bridges to the relay.** `S` (and `ptln start`) hosts a session via
  `ptysess`, which is built for many simultaneous viewers. Guests join over an **end-to-end
  Noise channel**; the **relay only ever sees ciphertext** and holds no key. The session key
  rides in the join link's `#` fragment.
- **The control plane is hosted and separate** (this repo is the client). It powers accounts,
  join links, and presence — it never receives your terminal stream.

> **Encryption note:** terminal sessions are end-to-end encrypted and the relay is blind. The
> control plane escrows the session key to power web/invite links, so this is "encrypted, we
> don't read it," not zero-knowledge. See `SECURITY.md`.

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
