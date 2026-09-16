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

`partyline` (`ptln`) is a single Go binary. Its two front doors:

1. **`ptln` — your AI session manager** (the front door). Find, resume, and run every
   Claude Code / Codex / Gemini / Antigravity / `llm` session on your machine from one
   launcher — `ptln new <tool>` starts a fresh one — and host several at once in a single
   multiplexed terminal instead of a tab per agent.
2. **`ptln start` — share any session, live.** Hand a teammate a link and they watch your
   terminal in real time over an end-to-end-encrypted, blind relay — pair on a bug, review an
   agent's work, run an incident.

Local-first: your sessions, terminal data, and code stay on your machine — nothing you type
or run is sent anywhere unless you explicitly share a session. The CLI does phone home for
exactly two small things, both disclosed and both optional:

- a once-a-day anonymous ping carrying only `{install_id, version, os}` — no paths, no
  content, no account identity. You're shown a notice before the first one ever sends.
  Opt out: `export PARTYLINE_TELEMETRY=0` (or the standard `DO_NOT_TRACK=1`).
- a version check so `ptln` can tell you an update exists.
  Opt out: `export PARTYLINE_NO_UPDATE_CHECK=1`.

Full details: [partyline.sh/docs/telemetry](https://partyline.sh/docs/telemetry). The optional
daemon (`ptln daemon enable`) and session sharing are separate, explicit opt-ins.

Beyond those, the same binary carries the rest of the toolkit — autonomous single-task
runs (`ptln work`) and backlog cranking (`ptln crank`), the Planning agent (`ptln plan`),
parties (humans + agents on one channel), Context Threads (shared memory), an org skill
library (`ptln skill`), a macOS menu-bar tray, and `ptln state` for scripts. `ptln help`
and `ptln man` cover all of it.

**There is no hosted partyline.** `partyline.sh` serves documentation and the installer,
nothing else — there is no sign-up, no hosted app, and no database behind it. The planning
board, the Build / Ship pipeline, teams, projects and parties live on a partyline **server
you run**, which this repo builds:

```sh
ptln server install                 # preflight, plan, apply, then verify the box serves
ptln server install --dry-run       # print the plan and stop, having written nothing
ptln login https://ptln.example.com # point this CLI at your box and pin its identity key
```

The session manager, `ptln start`, `ptln work` and `ptln crank` all run without a server.

## `ptln` — the AI session manager

Every AI CLI scatters its sessions across its own on-disk store. `ptln` reads them all
and puts them in one interactive launcher:

```sh
ptln                      # browse every session (claude · codex · gemini · antigravity · llm)
ptln new claude           # start a fresh session (add --thread <id> for shared context)
ptln --resume             # reopen the exact set you had open last time
ptln llms <id> [<id>…]    # scripting: open one or several straight into the multiplexer
```

Inside the launcher:

- **Browse + search** every session with rich detail — project, git branch, model, tokens,
  first prompt, recent messages, MCP servers, skills. Sort by **last used**; live sessions
  are flagged ⏳ waiting-for-you vs ● running.
- **Run several in one terminal.** Open sessions as windows in one multiplexer and switch
  from the tab bar — **`ctrl-\ ←/→`** (⏎ to commit) or **`ctrl-\ 1`–`9`** to jump. The same
  `ctrl-\` prefix also does **`[`** scroll-back · **`c`** record/view shared context ·
  **`s`** share this session (view-only) · **`o`** launcher · **`x`** close · **`q`** quit. No more tab-per-agent.
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
ptln party                          # interactive: start a new party, or join a team one
ptln party '<link>' --name dev      # bring an agent into a shared humans+agents channel
```

Address people and agents by name (`@name` / `@all` / `@any`); the runner wakes an agent
only when it's addressed. Parties share a working doc agents propose edits to and humans
approve. Run `ptln party` with no arguments for an interactive launcher (pick team + mode,
or join a party your team is already running). (Also hosted from the web/Slack; see the docs.)

## Context Threads — shared memory for your agents

Decisions, constraints, and contracts get lost across people, machines, and tools.
**Context Threads** (Common Ground) are a team-scoped, attributed feed of those facts that
any session can read and add to:

```sh
ptln thread new "checkout rework"   # a private-by-default thread; `share` it with the team
ptln new claude --thread <id>       # launch an agent wired to it — reads it at startup,
                                    # and can recall/remember more via MCP tools
```

Facts are attributed and versioned; agents only ever see confirmed, live ones. Manage the
timeline (edit / replace / prune / promote to a project) on the web, or with `ptln thread`.

**Remote launch (early access).** Instead of running `ptln party` by hand, a teammate can
click **Add agent** on the party page and start a grounded agent on *your* machine, in the
right project dir — after you opt in (`ptln daemon enable` + `add-project`) and approve each
launch in `ptln daemon run`. The control plane only ever sends a project *label*, never a
path or command (reference-not-command); the agent runs with read-only tools. See the docs.

## Import your backlog — use your LLM

Your roadmap lives in Jira, Linear, Productboard, GitHub, or a spreadsheet. partyline is the execution layer, not a second backlog, so bring items across and keep them linked:

> **you:** import the P1 bugs from our Jira board
> **claude:** *[reads Jira via your Jira MCP, calls `import_work_item` ×7]* — Started 7 planning sessions.

There is **no Jira integration to set up**. No OAuth, no API key, no per-tool connector. Your LLM already has your tracker connected — it *is* the integration. So this works for any tracker you can read, partyline never holds a credential for it, and a vendor changing their API can't break something that was never built.

Each ticket opens a **planning session** seeded with it verbatim — not a task. A ticket is a statement of a problem, not something an agent can build; you have the conversation, and *that* produces buildable work. Re-importing resumes the same session rather than starting a second one.

Full docs: <https://partyline.sh/docs/import>

## API keys & webhooks — connect the rest of your stack

Other software can hear about work here, and start work here.

- **Outbound webhooks** tell your automation when a run finishes, fails, or needs approval — signed (HMAC over `timestamp.body`, so a captured request can't be replayed), retried with backoff, and auto-disabled after repeated failures.
- **Inbound triggers** let a Sentry alert or a support ticket start a run: `POST /api/v1/t/<slug>` with a team key.
- **A read API** (`/api/v1/events`) is where a destination fetches the detail.

A webhook carries **ids and links, never your content**, so nothing your team writes ends up in a third party's request logs. And a trigger holds the project, machine and instruction a human configured — the caller supplies only data about what happened, which reaches the agent fenced and labelled as untrusted input. Without that split, "let other software start work" would just be an HTTP endpoint that runs arbitrary agent work in a repo of the caller's choosing.

Keys are team-scoped with explicit permissions, and there is deliberately **no write permission for runs**: nothing outside partyline can approve, merge, or ship.

Full docs: <https://partyline.sh/docs/webhooks>

## Architecture

```
                       YOUR MACHINE  (local-first, no daemon)

   ptln  ── AI session manager + multiplexer
   ┌──────────────────────────────────────────────────────────────┐
   │ reads each tool's own on-disk session store:                  │
   │   ~/.claude · ~/.codex · ~/.gemini · antigravity · llm        │
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

   YOUR partyline server (built from this repo, run by you): web + /api/v1 +
   Postgres — accounts, join links, presence, the board.  `ptln server install`
   stands it up.  Never sees your terminal bytes.
```

How it fits together:

- **The session manager is entirely local.** It parses each tool's own session files (no daemon, no
  network) and runs the sessions you open as child processes, each on its own PTY driven
  through a VT emulator. Switching repaints the target child's `Snapshot()`; only the active
  child's output reaches your screen.
- **Sharing bridges to the relay.** `S` (and `ptln start`) hosts a session via
  `ptysess`, which is built for many simultaneous viewers. Guests join over an **end-to-end
  Noise channel**; the **relay only ever sees ciphertext** and holds no key. The session key
  rides in the join link's `#` fragment.
- **The control plane is your own server.** This repo holds both halves: the CLI at the root and
  the web app + self-host stack under `web/` and `deploy/stack/`. The server powers accounts,
  join links, presence and the board — and never receives your terminal stream. Nobody but you
  operates one; see [`/docs/self-host`](https://partyline.sh/docs/self-host).

> **Encryption note:** terminal sessions are end-to-end encrypted and the relay is blind. Your
> instance escrows the session key to power web/invite links, so this is "encrypted, and the
> relay cannot read it," not zero-knowledge. See `SECURITY.md`.

## Licence

Two licences, drawn along one line: **the client is MIT, the server is ELv2.**

| | |
|---|---|
| **`ptln`, the CLI** | **MIT** — published at [partyline-sh/cli](https://github.com/partyline-sh/cli). Use it however you like, against any instance. |
| **This repository** (web app, server, self-host stack) | **[Elastic Licence 2.0](LICENSE)** |

ELv2 is short and permissive in every direction that matters to a normal user.
You may read the code, run it, modify it, and self-host it — including
commercially, inside your own company, with no seat count and no key to ask
anyone for. Self-hosting is not a fallback here; it is the only way the server
runs. [`/docs/self-host`](https://partyline.sh/docs/self-host) is the guide.

The one thing it forbids is offering this software to third parties as a hosted
or managed service — i.e. selling partyline-as-a-service. That is the only
restriction.

If you are unsure whether your use is fine, it almost certainly is; open an issue
and ask rather than guessing.

## Install

```sh
# macOS (Homebrew)
brew install partyline-sh/tap/partyline

# Linux / macOS (script)
curl -fsSL https://partyline.sh/install.sh | sh
```

`partyline` installs as both `partyline` and the short alias `ptln`.

## License

[MIT](LICENSE). This is the open-source partyline client (CLI + blind relay), mirrored
from the partyline monorepo. The web control plane is a separate, hosted service.
