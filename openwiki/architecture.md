# Architecture

How the pieces fit, who talks to whom, and the rules that hold it together. Deep dives live in
[`docs/`](../docs); this page is the connective tissue.

## Components

| Component | Lives in | Role |
|-----------|----------|------|
| **`ptln` CLI** | repo root (`package main`) + `internal/` | The local front door: session manager/mux, daemon, threads, parties, runs. |
| **Control plane (web)** | `web/` (Next.js App Router) | Auth, the `/api/v1` API, the fleet/projects/parties/threads UI, and the daemon's SSE stream. |
| **Relay** | `internal/relay` + `deploy/` | Blind TCP relay that splices E2EE session traffic between host + joiners. |
| **Daemon** | `daemon*.go` | Owner-run resident that the web can ask to launch agents — under reference-not-command. |
| **Database** | `supabase/migrations/` | Postgres + RLS. Migrations are the source of truth for schema. |

## Key data flows

**Session (E2EE shared shell).** Host runs `ptln start`; a 256-bit key is generated locally and rides
the join link's `#k=` fragment (the relay never sees it). Host + joiners run a Noise `NNpsk0`
handshake; the relay splices ciphertext. Identity: a signed-in joiner presents a control-plane-signed
Ed25519 assertion over the channel. Code: `internal/wormhole`, `internal/relay`, `join_client.go`.

**Remote launch (web → your machine).** The daemon holds an outbound SSE stream to
`/api/v1/daemon/stream`. The server pushes **events that are references, never commands**: `launch` /
`accepted` (a project label + a single-use join ref), `run` (a worklist), `kill`, `restart`,
`assign_project` (a candidate *handle* to bind), `relabel_project` (two label strings). The daemon
resolves each against its **local registry** and only executes after owner approval. Code:
`daemon.go` (stream handler), `daemon_projects.go` (candidates/assign/relabel), server side in
`web/src/app/api/v1/daemon/*` + `web/src/lib/api/daemon.ts`.

**Heartbeat / Fleet.** Every ~60s the daemon POSTs a **metadata-only** snapshot (version, OS,
advertised project labels + dir *basenames*, assignable candidates) to `/api/v1/daemon/heartbeat`.
The server rebuilds it from an explicit allow-list — no absolute path or secret can land. The fleet
view (`web/src/lib/api/fleet.ts`) reads it. Usage telemetry rolls up from here + an anonymous
install-id ping (`web/src/lib/api/admin.ts`, `/admin/usage`).

**Runs (crank).** A run is "execute this worklist on that daemon in this thread." crank drives tasks
one at a time, each in its own git worktree, through a two-layer **verify gate** (executable checks +
an adversarial reviewer) and an **acceptance gate** that routes failures to `needs_approval` before
anything merges. Code: `crank.go`, `verify.go`, `runlog.go` (hash-chained tamper-evident ledger).

**Context Threads (Common Ground).** A thread is a team-scoped, private-by-default feed of seam facts
(decision | constraint | contract | question | note). Agents read/write it via the `cg-mcp` MCP server
(`recall` / `remember` / `read_context`), wired at session launch. Facts can **graduate** into a
project's durable canon. Code: `thread.go`, `cg_mcp.go`; server in `web/src/lib/api/threads.ts` +
`projects.ts`; design in [`docs/COMMON-GROUND.md`](../docs/COMMON-GROUND.md).

## The invariants (do not break)

- **Reference-not-command** — see quickstart. Any new daemon event must carry a label/handle/ref, never
  a path or argv. Tests guard the handle-resolution (`daemon_projects_test.go`).
- **Blind relay + authenticated E2EE** — the relay must never receive a key; the channel is Noise +
  Poly1305 (tamper-evident).
- **No secrets in the heartbeat** — the snapshot is rebuilt from an allow-list; only labels, version,
  OS, and dir *basenames* leave a machine.
- **Migrations before code** — never merge web code reading a column whose migration a human hasn't
  applied to prod.
- **Single-org model** — one org per user; RBAC roles are `owner | admin | member` on `org_members`.

## Deployment

- **web** auto-deploys on push to `main` (`.github/workflows/deploy-hetzner.yml` → Hetzner box,
  docker-compose; runtime env in `/opt/partyline/.env` on the box, plus a few CI-managed vars).
- **CLI** ships on a `v*` tag (`release.yml` → `partyline-sh/cli` + Homebrew). **Merging does not
  release the CLI** — a tag does.
