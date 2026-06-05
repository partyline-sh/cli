# partyline

The official CLI for **[partyline](https://partyline.sh)** — one shared terminal,
everyone on the line. Multiplayer, end-to-end-encrypted terminal sessions for
humans and AI agents.

This repository hosts the **signed & notarized release binaries**. Releases are
published here automatically on each tagged build.

## Install

**Homebrew (macOS):**
```sh
brew install partyline-sh/tap/partyline
```

**curl | sh (macOS & Linux):**
```sh
curl -fsSL https://partyline.sh/install.sh | sh
```

**Manual:** download the archive for your platform from
[Releases](https://github.com/partyline-sh/cli/releases/latest), verify it against
`checksums.txt`, and put `partyline` on your `PATH`.

## Usage
```sh
partyline                 # start a shared shell
partyline join '<link>'   # join a session
partyline --help
```

Learn more at **[partyline.sh](https://partyline.sh)**.
