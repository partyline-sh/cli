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

A session manager for AI coding agents, with repository memory, a chat between sessions, and loops
that review an agent's work.

`partyline` (`ptln`) is one Go binary for macOS and Linux:

| Part | What it does |
|---|---|
| **Sessions** (`ptln`) | Lists every Claude Code, Codex, Gemini, opencode, goose and Antigravity session on the machine, across directories and git worktrees, and runs them in one terminal hosted by tmux. Sessions keep running when the terminal closes. |
| **Repo memory** (`ptln memory`) | Keeps facts about a repository as one markdown file each in `memory/` inside it, and writes them into `AGENTS.md`, `CLAUDE.md` and `GEMINI.md` so every engine reads them at session start. |
| **Chat** (`ptln chat`) | One room where you and your running sessions post messages, and the sessions answer each other. |
| **Loops** (`ptln loop`) | A session builds work in rounds; your checks run on each round, then other models review it. |

There is no server and no account. partyline sends no telemetry. The requests it makes itself:

| Request | When |
|---|---|
| GitHub release check | once a day; `PARTYLINE_NO_UPDATE_CHECK=1` turns it off. A new release opens a popup once: update now, skip it, or update automatically |
| Automatic update | only when turned on in that popup: Homebrew or the install script, run by the watcher |
| `gh pr checks` | for a loop whose session recorded its PR, at most every 2 minutes |
| Jev (api.typesafe.ai) | only with a Jev key saved in the loop settings: a disputed loop finding, its dispute and up to 6000 characters of its brief |

```sh
curl -fsSL https://partyline.sh/install.sh | sh   # macOS and Linux
brew install partyline-sh/tap/partyline          # macOS
```

## Sessions

```sh
ptln                                    # open the session manager
ptln new claude                         # start a session
ptln new codex --worktree spike-auth    # start one in a new git worktree
ptln --resume                           # reopen the sessions open when you last quit
```

In the manager, `⏎` opens a session after a permission-mode picker, `space` selects several to open
together, `x` deletes (with a confirmation that shows uncommitted files and live sessions in the same
tree), and `?` lists every key. In a session, `ctrl-\` opens a menu with every command.

## Repo memory

```sh
ptln memory build --apply      # project overview, build/test/CI facts, open questions
ptln memory add gotcha "The importer rejects CSV files with a byte-order mark."
ptln memory edit <id> "..."    # rewrite a fact that turned out wrong
ptln memory hooks              # which engines refresh facts during a session
```

partyline writes facts to your working tree and never commits; you commit them with the change they
describe. `git log -p memory/` is the history. A fact about a machine, a work item's status or one
session is refused with the reason. In a repository without `memory/`, a partyline session offers
once to set it up.

## Chat

```sh
ptln chat
/add 10 11          # in the room: add the sessions in tabs 10 and 11
```

Messages reach a working Claude session through its Stop hook, and an idle one through a background
listener that wakes it. Codex and sessions with nothing listening are sent a one-line pointer to
`ptln chat read`, typed only into an empty, idle input box you are not looking at.

## Loops

Tell a Claude, Codex or Gemini session running in partyline: "use a partyline loop for this". The
session reads `ptln loop guide`, writes a brief with its checks, and starts the loop.

- Before the first loop, choose who builds and who reviews (engine, model, effort) in the loop
  settings: `ctrl-\` then `,`.
- By default loops decide for themselves: a brief is approved, and a disputed finding is ruled on by
  a model that did not build the work. Set Decisions to "ask me" to approve and rule yourself in
  the approval popup (`ctrl-\` then `a`, behind a code shown on screen).
- A batch is several ordered steps. partyline starts contractors to build each step and commits
  each step that passes in the batch's checkout. It never pushes.
- `ctrl-\` then `l` opens the Loops board. `d` twice on a card deletes that loop
  (`ptln loop delete <id>`).

## Documentation

- <https://partyline.sh/docs>
- `ptln man`, and `ptln <command> --help`
- <https://partyline.sh/llms-full.txt>: the documentation as one plain-text file

## Building

```sh
go build -o partyline .                    # the CLI
cd web && npm install && npm run build     # the documentation site
```

`make check` runs every check CI runs. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

MIT, for the whole repository. See [LICENSE](LICENSE). The CLI source is published to
[partyline-sh/cli](https://github.com/partyline-sh/cli) with every release.

## Mobile remote

The read-only phone app is served by your Cloudflare relay. Open its URL, pair
from `ptln remote pair`, then use Add to Home Screen. It shows locally paired
machines, sessions, live panes, the Loops board, contractors and chat. Opening it
again reconnects and redraws from fresh snapshots. It does not send terminal
input, chat messages or decisions in this version.

Cloudflare carries ciphertext, routing ids, message sizes and timing, IP
addresses and (when notifications are enabled) push endpoints. A passive relay
cannot read pane or chat content. Threat 1: the relay **serves the app's code**;
a compromised relay or Cloudflare account can change that code and steal phone
keys or content. `ptln relay deploy` records uploaded asset SHA-256 digests in
`relay.json`; `ptln relay status` and the pairing approval popup compare served
assets against those digests. This is detection, not prevention: a malicious
relay can serve different bytes to the machine and phone. Joined machines have
no local deploy baseline until they deploy.

On Cloudflare's free plan, requests fail after a daily limit until 00:00 UTC.
The app needs the network; it does not cache snapshots or executable assets for
offline use. Phone pairing records remain in browser storage; if iOS clears
that storage, pair again. Real iPhone install, background/reopen, storage,
passkey and pairing checks must be completed before the next batch.

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
