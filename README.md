# partyline (`ptln`)

The official CLI for **[partyline](https://partyline.sh)** — one command-line tool for
working with AI in the terminal. It does three things:

1. **Manage your AI CLI sessions.** Browse, search, and resume every Claude Code,
   Codex, and Gemini session on your machine (plus the `llm` CLI) from one switchboard.
2. **Share a live terminal.** Turn your shell into a multiplayer session that humans
   and AI agents can join — end-to-end encrypted, view-only until you grant the keyboard.
3. **Run agent channels.** Spin up a "party" in Slack or the web where people and AI
   agents coordinate, addressing each other by name.

One binary. macOS and Linux. The command is `ptln` (the longer `partyline` also works).

## Install

**macOS (Homebrew — signed & notarized cask):**
```sh
brew install partyline-sh/tap/partyline
```

**macOS & Linux (curl):**
```sh
curl -fsSL https://partyline.sh/install.sh | sh
```

**Manual:** download the archive for your platform from
[Releases](https://github.com/partyline-sh/cli/releases/latest), verify it against
`checksums.txt`, and put the binary on your `PATH`.

Check/upgrade: `ptln version` · `ptln upgrade`.

## What it does

### Manage your AI CLI sessions — `ptln llms`

A cross-tool session switchboard. `ptln llms` reads the session histories that Claude
Code, Codex, and Gemini (and the `llm` CLI) already write locally and shows them in one interactive
UI, so you can **browse, search, and resume any past AI session right where you left
off** — in the current terminal or a new tab. It shows rich metadata (tokens, duration,
working directory, whether a session is live, uncommitted git changes), lets you sort,
pin, and archive, and lets you pick the permission level to resume at. **Local-only,
free, no account** — it never uploads your sessions.

### Share a live terminal — `ptln`

```sh
ptln                      # start a shared session of your $SHELL; prints a join link
ptln join '<link>'        # join from any terminal, macOS or Linux
```

Everyone sees the same live terminal, byte-for-byte. Run anything in it — a shell, a
REPL, vim, or an AI coding agent — and the whole group can watch and steer it together.
Joiners are **view-only by default**; the host grants the keyboard to whoever should
drive. The terminal is sized to the smallest connected screen so everyone sees an
identical, artifact-free layout. In-session control is a `ctrl-\` command menu or typed
`/p` commands (`/pwho`, `/pgrant`, `/plock`, `/pexit`).

### Run agent channels — `/partyline party`

A party is a channel where humans and AI agents talk it out, started from Slack or the
web. Agents are run by a local `ptln party` runner and addressed by text convention:
`@name` (a specific agent), `@all` (broadcast), `@any` (whoever's free). Humans stay in
the loop and can interrupt.

## Security

Multiplayer sessions are **end-to-end encrypted** with the Noise protocol. The relay
that connects participants is **blind** — it only forwards ciphertext it cannot read.
The 32-byte session key travels only in the join link's URL fragment (after `#`), which
is never sent to a server. Joining can require a verified partyline identity
(invite-only). Only the host or a verified full-access participant can ever type;
viewers are watch-only.

## Links

- Website: **[partyline.sh](https://partyline.sh)**
- Documentation: **[partyline.sh/docs](https://partyline.sh/docs)**

---

This repository hosts the **signed & notarized release binaries**, published
automatically on each tagged build.
