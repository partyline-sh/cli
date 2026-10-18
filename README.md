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

**One terminal for every AI coding session — and the repo remembers what the team learned.**

`partyline` (`ptln`) is a single Go binary that does two things:

1. **`ptln` — your AI session manager.** Find, resume and run every Claude Code / Codex /
   Gemini / opencode / goose / Antigravity session on your machine from one launcher, across
   directories and git worktrees. Sessions outlive the terminal they were started in.
2. **`ptln memory` — what the team knows about this repo, kept in this repo.** One markdown
   file per fact under `memory/`, committed with the code. Every engine reads it at session
   start through its own `AGENTS.md` / `CLAUDE.md` / `GEMINI.md`; engines with a hook refresh it
   every turn.

There is no server, no account and no hosted product. `partyline.sh` serves documentation and
the installer, nothing else. Nothing you type or run leaves your machine. The CLI phones home
for two small things, both disclosed and both optional: a once-a-day anonymous ping carrying
only `{install_id, version, os}` (opt out: `PARTYLINE_TELEMETRY=0` or `DO_NOT_TRACK=1`) and a
version check (`PARTYLINE_NO_UPDATE_CHECK=1`). Details: [partyline.sh/docs/telemetry](https://partyline.sh/docs/telemetry).

```sh
curl -fsSL https://partyline.sh/install.sh | sh   # macOS + Linux
brew install partyline-sh/tap/partyline          # macOS
```

## `ptln` — the AI session manager

Every AI CLI scatters its sessions across its own on-disk store. `ptln` reads them all and
puts them in one interactive launcher:

```sh
ptln                      # browse every session on this machine
ptln new claude           # start a fresh one
ptln new codex --worktree spike-auth   # in an isolated git worktree
ptln --resume             # reopen the exact set you had open last time
```

Inside the launcher:

- **Browse + search** every session with rich detail — project, branch, model, first prompt,
  recent messages. Live sessions are flagged; sort by last used.
- **Run several in one terminal.** Open sessions as windows in one tmux-hosted multiplexer
  and switch from the tab bar; `ctrl-\` opens the menu with every command and its key.
- **Resume with the right permissions** — a quick picker before the agent picks up.
- **Delete** (`x`) confirms first, showing age, worktree, uncommitted files and whether anything
  else is live in the same tree; a running session cannot be deleted. Deleted sessions go to a
  trash directory, not oblivion.
- **New** (`n`), **pin** (`p`), **split** (`|`), **filter** (`/`), **docs** (`D`), **every key** (`?`).

## `ptln memory` — the repo remembers

```sh
ptln memory add gotcha "Xero's /Reports/TrialBalance ignores the date parameter."
ptln memory edit <id> "...what turned out to be true instead"
ptln memory build          # seed a legacy repo from evidence; a dry run until --apply
ptln memory hooks          # which engines refresh mid-session, and which need trusting
```

Facts are files in the repo: cloning the code clones what the team learned about it, and a
fact is reviewed in the same pull request as the change that taught it. **partyline never
commits your repository** — a fact lands in your working tree for you to commit.

Facts are **mutable**. A claim that turns out wrong is rewritten, not annotated with a
correction beside it; `git log -p memory/` is the audit trail. A fact has to pass a gate at
write time — *would this still be true and useful to someone who opens this repo in three
months and was not here?* — so host state, machine config and work-item status are refused.

`ptln memory build` records only what it can cite (layout, entry points, build and test
commands, CI, migrations, churn) and turns everything that would require knowing *why* into
an open question instead of a guess.

Full docs: <https://partyline.sh/docs/memory>

## Building

```sh
go build -o partyline .     # the CLI
cd web && npm install && npm run build   # the documentation site (static Next app)
```

`make check` runs every gate CI runs. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

MIT — the whole repository. See [LICENSE](LICENSE). The CLI is also mirrored to
[partyline-sh/cli](https://github.com/partyline-sh/cli) on every release.

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
