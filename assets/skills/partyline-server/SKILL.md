---
name: partyline-server
description: Administer the self-hosted partyline instance on this machine — install, upgrade, backup, status, tunnels, ports, DNS, storage. Use when asked to set up, fix, reconfigure, or check a partyline server.
---

# Administering a partyline server

Everything routes through `ptln server`. Prefer it over hand-editing files: its commands assert
their own effects and reconcile on re-run.

## Read first, always

    ptln server status    # what is running; every bad line carries the logs command
    ptln server doctor    # which features this box's environment configures

The install lives at `$PARTYLINE_DIR`, else `/opt/partyline`, else `~/partyline`. It is a
directory holding `docker-compose.yml` and `.env`.

## Install / reconfigure

    ptln server install --yes --site <url> [flags]

Interactive runs open a menu; as an agent, pass `--yes` with explicit flags:

| Flag | Meaning |
|---|---|
| `--site URL` | the address people use. Must be reachable FROM INSIDE containers — never localhost or 127.x |
| `--http-port / --https-port / --relay-port` | host ports; pick free ones |
| `--bind ADDR` | interface; `127.0.0.1` when a tunnel fronts it |
| `--tls auto\|acme\|internal\|off` | `acme` needs public 80/443; `internal` = Caddy's own CA; `off` = plain HTTP (LAN or tunnel) |
| `--dns ADDR` | resolver for internal-only names; containers use it too |
| `--no-minio` | skip attachment storage |
| `--dry-run` | print the plan, write nothing |

Re-running reconciles. It never rewrites `.env` or an edited `Caddyfile`.

## Upgrade, backup

    ptln update && ptln server upgrade    # the whole upgrade path, correct order
    ptln server backup [--out FILE]       # streamed pg_dump + .env + Caddyfile, 0600, restore steps printed

## Tunnels (no public address needed)

    ptln server tunnel

Reads the box, prints tailored steps, and drives the Cloudflare create/route/config after
`cloudflared tunnel login`. Tailscale: it offers to run `tailscale serve` when logged in.

## Rules

- **Never** run `docker compose down -v` — `-v` deletes the database.
- **Never** print values from `.env`; refer to variables by NAME. It is every secret the box has.
- Do not edit `web/public/*` or files whose header says GENERATED.
- The site address decides sign-in: SITE_URL is what containers resolve to reach the identity
  provider. If sign-in fails, check `docker compose logs keycloak` and that SITE_URL resolves
  from inside a container before anything else.
- `.env` changes take effect after `docker compose up -d` (recreates changed services).
- A `denied` pulling images: the public registry is Docker Hub; `ghcr.io` copies are internal.

## When it does not work

    cd <install-dir> && docker compose ps
    docker compose logs <service> --tail=50

Services: web, postgres, postgrest, keycloak, redis, caddy, relay, minio, minio-init. `minio-init`
exiting is normal (one-shot).
