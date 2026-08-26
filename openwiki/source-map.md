# Source map

Where things live. Paths are relative to the repo root.

## CLI (`package main`, repo root)

| Area | Files |
|------|-------|
| Entrypoint / dispatch | `main.go` (routes every `ptln <cmd>`) |
| Session manager + mux | `llms.go`, `llms_mux.go`, `llms_tui.go`, `llms_index.go`, `llms_open.go`, `llms_daemon.go` |
| Sessions (host/join, E2EE) | `join_client.go`, `join_menu.go`, `share_tab.go`, `shell.go` |
| Daemon (remote launch) | `daemon.go`, `daemon_projects.go` (candidates/assign/relabel), `daemon_service.go` |
| Context Threads | `thread.go`, `repobind.go` (`.partyline.json` + AGENTS.md/CLAUDE.md breadcrumb), `cg_mcp.go`, `cg_menu.go` |
| Parties | `party*.go`, `party_mcp.go`, `join_mcp.go` |
| Runs / automation | `crank.go`, `verify.go`, `runlog.go`, `keepgoing.go`, `work*.go` |
| Update / telemetry | `update.go`, `telemetry.go` (anonymous opt-out ping) |
| Accounts / help | `account_cmds.go`, `help.go`, `manpage.go`, `helpers.go` |

## `internal/` packages

| Package | Role |
|---------|------|
| `internal/api` | The CLI's thin client for the control plane (`/api/v1`). `DaemonStream`, `Heartbeat`, thread/party/run calls. |
| `internal/relay` | The blind relay server (splices E2EE traffic; holds no key). |
| `internal/wormhole` | Noise `NNpsk0` E2EE channel for sessions. |
| `internal/sshd` | SSH front for the relay (`ssh code@pppp.sh`). |
| `internal/ptysess`, `internal/ptymux` | Terminal engine (PTY + vt emulator) and the local multiplexer (`ptln mux`). |
| `internal/gitwt` | Git worktree helpers (per-task isolation for runs). |
| `internal/identity` | Local identity / key handling. |
| `internal/obs` | Sentry wiring (no-op unless `SENTRY_DSN`). |

## Control plane (`web/`, Next.js App Router)

| Area | Path |
|------|------|
| API routes | `web/src/app/api/v1/**` (daemon stream/heartbeat/launch/assign, projects, parties, threads, telemetry, version) |
| App pages | `web/src/app/**` — `fleet/`, `projects/`, `parties/`, `threads/`, `runs/`, `admin/usage/`, `dashboard/` |
| Server helpers | `web/src/lib/api/*.ts` — `fleet.ts`, `daemon.ts`, `projects.ts`, `orgs.ts`, `admin.ts`, `party*.ts`, `threads.ts`, `runs.ts`, `billing*.ts`, `entitlements.ts` |
| Marketing + docs | `web/src/app/(marketing)/**` (incl. `docs/`) |

## Data + infra

| Area | Path |
|------|------|
| Schema | `supabase/migrations/NNNN_*.sql` (source of truth; human-applied to prod) |
| Deploy | `deploy/docker-compose.yml`, `deploy/Caddyfile`; `.github/workflows/deploy-prod.yml` + `deploy-staging.yml` (web), `release.yml` (CLI on tag) |
| Deep design docs | `docs/` — `ARCHITECTURE.md`, `COMMON-GROUND.md`, `FLEET.md`, `PARTY.md`, `BILLING.md`, … |

## Conventions

- Dense **header comments** on most files explain *why*, not just *what* — read them first.
- Tests sit next to their subject (`*_test.go`); the security-critical ones (handle resolution, no-leak
  snapshot, run ledger) are worth reading as executable specs.
