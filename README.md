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

There is no Partyline-hosted server and no Partyline account. Optional phone access
uses a relay you control. Partyline sends no telemetry. The requests it makes itself:

| Request | When |
|---|---|
| GitHub release check | once a day; `PARTYLINE_NO_UPDATE_CHECK=1` turns it off. A new release opens a popup once: update now, skip it, or update automatically |
| Automatic update | only when turned on in that popup: Homebrew or the install script, run by the watcher |
| `gh pr checks` | for a loop whose session recorded its PR, at most every 2 minutes |
| Your relay and optional Cloudflare API | only when you configure phone access; session/chat traffic is encrypted |
| Local multicast discovery | while private gateway discovery/advertising is enabled; friendly name and HTTPS origin, no session content |
| Let's Encrypt ACME | only with the explicit `relay serve --acme` option, to request the gateway certificate |
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

The native iOS/Android app has Sessions, Loops, Chat and Computers. The iOS app is
currently distributed to invited testers through TestFlight; Android development
builds use [mobile/README.md](mobile/README.md). A browser client is also available
at your relay's HTTPS address and can be added to the home screen.

Run `ptln relay setup`, or press **s** in Relay settings, to choose a private
Go gateway, a trusted HTTPS proxy/VPN route or Cloudflare. The Go relay is built
into the binary and needs neither Node.js nor Docker on its host. Hosting a
separate gateway preserves an existing client connection. Keep the gateway awake
and its HTTPS route reachable; DNS alone cannot bypass carrier-grade NAT.

Pair each computer separately with `ptln remote pair`, compare the four-digit
phone code, and approve on the computer. A gateway's six-digit membership check
is separate from phone pairing. Revoke a phone with `ptln remote revoke <id>`.

The native app can send chat, insert or submit terminal input, and attach photos
or files to a draft. Uploads are limited to 20 MiB per file and a 200 MiB per-phone
cache. A confirmed upload adds its computer-side path; it never sends the draft
for you. Files are private and non-executable. Unknown input delivery is never
replayed on reconnect; retrying may run the command twice.

Phone layout is opt-in and narrows the same single-pane window
on the computer. Readable/Fit width only change the display. The computer
restores a lost phone layout after its 30-second lease; existing output keeps
its previous wrapping. The native Loops board has multi-select stage filters.

Phone content is encrypted between devices. The relay sees routing identifiers,
network addresses, timing and sizes. A compromised relay can serve modified PWA
code; native UI code comes from the installed app. Asset checks detect changes
against a saved baseline but cannot prevent a malicious relay from serving
different code to different clients. See [Security](SECURITY.md).

Native apps update through their distribution channel; a relay deploy does not
update an installed native app. Go gateway PWA updates need a new binary and a
service restart; Cloudflare PWA updates need a Worker redeploy. Connection errors
are in `~/.partyline/remote/session.log` (latest 200 lines).

The gateway, upload and phone-layout commands documented here require CLI
**v0.113.0 or later**. Native app distribution is a separate beta. See [Mobile remote](https://partyline.sh/docs/mobile-remote)
for setup, capabilities, storage paths and troubleshooting.

## Published source

This is a generated source snapshot of Partyline's CLI, blind relay and native
mobile client, mirrored after releases. Partyline runs on computers you control;
there is no Partyline-hosted control plane or Partyline account.

Install the signed CLI with the commands above, or build the source using the
requirements in `go.mod`. Native app builds are separate from the CLI binary;
see `mobile/README.md` when that source is present in this release.

The next release refreshes this mirror. Report bugs and discuss patches in
[the public issue tracker](https://github.com/partyline-sh/cli/issues); changes
are developed in the source repository rather than merged into this generated
history. The licence is [MIT](LICENSE), and vulnerability reports follow
[SECURITY.md](SECURITY.md).
