# partyline — quickstart

> Agent-facing overview of this repository. Start here, then follow the links.
> Hand-written (not generated); keep it current when the shape of the system changes.

## What partyline is

partyline is **multiplayer coordination for humans + AI coding agents, with oversight built in**.
It is not one app — it's a small set of composable pieces sharing one control plane:

- **Sessions** — end-to-end-encrypted shared terminals over a *blind* relay. Host a shell, share a
  link, others join and watch/drive. See [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md), security in
  [`docs/` security notes](../docs).
- **Parties** — a channel where humans and AI agents coordinate (Slack + web + CLI runner). Agents are
  episodic: woken when addressed. See [`docs/PARTY.md`](../docs/PARTY.md).
- **Context Threads (Common Ground)** — shared, durable memory of *decisions, constraints, and
  contracts* across people, machines, and engines. Distinct from code docs: this is the "why," not the
  "what." See [`docs/COMMON-GROUND.md`](../docs/COMMON-GROUND.md).
- **Projects** — the organizing lens: a *label* that joins a project's machines, runs, threads, and
  canon. Define/promote/edit them in the web.
- **Daemon + Fleet** — an owner-run daemon lets the web launch agents on your machine under a strict
  **reference-not-command** invariant. See [`docs/DAEMON-MVP.md`](../docs/DAEMON-MVP.md),
  [`docs/FLEET.md`](../docs/FLEET.md).
- **Runs / crank** — drive a worklist of tasks, each in its own git worktree, with verify + approval
  gates before anything merges.
- **mux** — one local terminal hosting many live LLM CLI sessions (`ptln mux` / `ptln llms`).

## The mental model

```
  people + AI agents
        │
   ┌────┴─────┐        ┌───────────────┐        ┌──────────────┐
   │  ptln    │  HTTPS │ control plane │  RLS   │  Supabase    │
   │  (CLI)   │───────▶│  (web/, Next) │───────▶│  Postgres    │
   └────┬─────┘        └──────┬────────┘        └──────────────┘
        │ E2EE (Noise)        │ SSE stream (reference, never a command)
   ┌────┴─────┐        ┌──────┴────────┐
   │  relay   │        │ your daemon   │  spawns gated agents on your machine
   │ (blind)  │        │ (ptln daemon) │
   └──────────┘        └───────────────┘
```

## Where to start reading

- **Whole-system design:** [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md)
- **CLI entrypoint + dispatch:** [`main.go`](../main.go) — every `ptln <cmd>` routes from here.
- **This overview's map of the tree:** [source-map.md](source-map.md)
- **How the pieces fit + the invariants that must never break:** [architecture.md](architecture.md)

## Non-negotiable invariants (read before changing these areas)

1. **Reference-not-command (daemon):** the control plane only ever sends a *label* / opaque *handle* —
   never a path or command. The daemon resolves it against its **own local registry**, and execution
   is owner-gated. `daemon.go`, `daemon_projects.go`.
2. **Blind relay / E2EE:** the relay forwards ciphertext and holds no key. `internal/relay`,
   `internal/wormhole`.
3. **Migrations are human-applied:** an agent writes `supabase/migrations/NNNN_*.sql`; a human applies
   it to prod *before* merging web code that reads the new columns.
4. **Single org per user:** every user has exactly one org; there are no teams-within-orgs.
