#!/usr/bin/env bash
# GENERATED — DO NOT EDIT.
#
# Source: internal/surface (vocabularies), internal/clispec (commands),
#         internal/surfacescan (routes, schema, environment).
# Regenerate: make surface-gen     Verify: make surface-check
#
# Hand edits are reverted by the next regeneration and fail CI in the meantime.
# To change what this says, change the declaration it is generated from.
#
# Published copy of scripts/env-bootstrap.sh — generated from the stack in this repo, so it
# cannot drift from the file a partyline box actually runs.
# One block differs from ours, replaced deliberately and marked in the script: the rule that gives
#   production port 22. That exists because pppp.sh:22 is compiled into every shipped CLI; on your
#   box it would collide with sshd, so the published copy defaults to 2222.
#
# Copy it to your box and edit YOUR copy; the banner above is about this one.
# Docs: https://partyline.sh/docs/self-host
# Fill in every value a partyline box can derive for itself. Runs ON THE BOX.
#
#   scp scripts/env-bootstrap.sh root@HOST:/tmp/ && ssh root@HOST '/tmp/env-bootstrap.sh https://partyline.sh'
#
# BASH, NOT SH. It uses `set -o pipefail` and $(( )); dash — which IS /bin/sh on Debian and Ubuntu —
# cannot do pipefail and exits immediately. Run it as ./env-bootstrap.sh or `bash env-bootstrap.sh`.
#
# The install directory, the ports, the interface and whether there is a reverse proxy at all are
# ALL choices, not assumptions — see usage() below. The defaults reproduce what partyline.sh's own
# boxes run: /opt/partyline, Caddy on 80/443, every interface.
#
# IDEMPOTENT: an existing value is never overwritten, so this is safe to re-run on a live box and
# safe to run on a half-configured one. It only ever ADDS what is missing.
#
# It handles three of the four categories in .env:
#   generated  — random secrets nobody needs to know (Postgres, session signing, tick, relay…)
#   derived    — the anon/service_role JWTs, which are SIGNED WITH SESSION_JWT_SECRET and therefore
#                cannot be copied between environments; they must be minted per box
#   known      — config that follows from the hostname (SITE_URL, PGRST_URL, OIDC_ISSUER)
#
# The fourth — real third-party credentials (Resend, and R2/S3 if you use one instead of the
# stack's own MinIO) — is deliberately NOT here.
# Append those to .env by hand (it is chmod 600), so they never pass through a terminal argument, a
# shell history or an agent transcript. This script prints a list of what is still missing at the
# end.
#
# IT IS A FILE, NOT A HEREDOC PIPED TO `bash -s`. Piping puts the script on stdin, and anything
# inside that reads stdin then eats the rest of it — bash hits EOF and exits 0, a silent no-op that
# looks exactly like success. That bug made three deploys "succeed" against an empty database.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: env-bootstrap.sh <SITE-URL> [--no-proxy]

  <SITE-URL>   the URL people will open, INCLUDING the port if it is not the scheme's default:
                 https://partyline.example.com        Caddy on :443, certificate from Let's Encrypt
                 https://partyline.example.com:8443    Caddy on :8443, TLS terminated somewhere else
                 http://100.84.12.9:8080              plain HTTP on a private network

  --no-proxy   do not run Caddy. The app is published directly on the SITE-URL's port and Keycloak
               on its own (KEYCLOAK_PORT, default that port + 1). Bring the stack up with
                 docker compose -f docker-compose.yml -f docker-compose.direct.yml up -d
               Plain HTTP on a private network only: there is no TLS anywhere in that mode.

  PARTYLINE_DIR=<dir>   install here instead of the directory this script is sitting in.
USAGE
  exit 1
}

SITE=""
NO_PROXY=0
for arg in "$@"; do
  case "$arg" in
    --no-proxy|--no-tls) NO_PROXY=1 ;;
    -h|--help) usage ;;
    -*) echo "env-bootstrap.sh: unknown flag $arg" >&2; usage ;;
    *) [ -z "$SITE" ] || usage; SITE="$arg" ;;
  esac
done
[ -z "$SITE" ] && usage
SITE="${SITE%/}"

# WHERE THE INSTALL LIVES — chosen, not assumed. This was an unconditional cd to /opt/partyline:
# correct for our own boxes and wrong for everyone else, who got "Permission denied" and then a
# cascade of errors from a script carrying on in the wrong directory. The order below is explicit
# beats inferred beats convention, and it PRINTS what it picked, because a script that writes
# secrets somewhere you did not expect is worse than one that refuses.
#
#   1. $PARTYLINE_DIR                        — you said so
#   2. the directory this script sits in     — if it holds a docker-compose.yml, that IS the install
#   3. the current directory                 — same test
#   4. /opt/partyline                        — what partyline.sh's own boxes use
here="$(cd "$(dirname "$0")" && pwd)"
if [ -n "${PARTYLINE_DIR:-}" ]; then
  install_dir="$PARTYLINE_DIR"
elif [ -f "$here/docker-compose.yml" ]; then
  install_dir="$here"
elif [ -f "$PWD/docker-compose.yml" ]; then
  install_dir="$PWD"
else
  install_dir="/opt/partyline"
fi
mkdir -p "$install_dir"
cd "$install_dir"
install_dir="$PWD"
echo "→ install directory: $install_dir"
touch .env && chmod 600 .env

# ── What the SITE URL decides ──────────────────────────────────────────────────────────────────
# Scheme, host and port are pulled apart once, here, because four different values downstream have
# to agree with them exactly: the ports compose publishes, Keycloak's KC_HOSTNAME, OIDC_ISSUER, and
# the realm's sslRequired. Deriving each one separately is how they drift.
scheme="${SITE%%://*}"
hostport="${SITE#*://}"
hostport="${hostport%%/*}"
case "$hostport" in
  \[*\]:*) site_host="${hostport%]*}]"; site_port="${hostport##*]:}" ;;   # [2001:db8::1]:8443
  \[*\])   site_host="$hostport";        site_port="" ;;                  # [2001:db8::1]
  *:*)     site_host="${hostport%:*}";   site_port="${hostport##*:}" ;;
  *)       site_host="$hostport";        site_port="" ;;
esac

have() { grep -q "^$1=." .env 2>/dev/null; }
put()  { have "$1" && return 0; printf '%s=%s\n' "$1" "$2" >> .env; echo "  + $1"; }
get()  { sed -n "s/^$1=//p" .env | head -1; }

echo "→ generated secrets"
# `openssl rand -base64` can emit '+' and '/'; base64url-ish output keeps these safe to paste into a
# connection string or a URL without escaping surprises.
rnd() { openssl rand -base64 48 | tr -d '\n=' | tr '+/' '-_' | cut -c1-"${1:-43}"; }
put POSTGRES_PASSWORD      "$(rnd 32)"
put AUTHENTICATOR_PASSWORD "$(rnd 32)"
put SESSION_JWT_SECRET     "$(rnd 43)"
put SESSION_KEY_WRAP       "$(rnd 43)"
put SLACK_STATE_SECRET     "$(rnd 43)"
put TICK_SECRET            "$(rnd 43)"
put RELAY_SECRET           "$(rnd 43)"

echo "→ derived PostgREST keys"
# These are HS256 JWTs signed with SESSION_JWT_SECRET — the SAME secret PostgREST verifies with
# (PGRST_JWT_SECRET in the compose file). They therefore cannot be shared between environments: a
# key minted for staging is meaningless to prod's PostgREST and vice versa. Mint, never copy.
#
# Shape matches what the boxes already run: {"role":…,"iss":"partyline","iat":…,"exp":…}, 10-year
# expiry — these are infrastructure credentials, not user sessions.
mint() {
  python3 - "$1" "$(get SESSION_JWT_SECRET)" <<'PY'
import base64, hashlib, hmac, json, sys, time
role, secret = sys.argv[1], sys.argv[2]
b64 = lambda b: base64.urlsafe_b64encode(b).rstrip(b'=')
now = int(time.time())
h = b64(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(',', ':')).encode())
p = b64(json.dumps({"role": role, "iss": "partyline", "iat": now, "exp": now + 315360000},
                   separators=(',', ':')).encode())
sig = b64(hmac.new(secret.encode(), h + b'.' + p, hashlib.sha256).digest())
print((h + b'.' + p + b'.' + sig).decode())
PY
}
put SUPABASE_SERVICE_ROLE_KEY "$(mint service_role)"
# PGRST_ANON_KEY is the fallback identity for an unauthenticated request. It used to arrive as the
# NEXT_PUBLIC_SUPABASE_ANON_KEY *build arg*, which is why no box has ever had it in .env — moving
# the config to runtime means it must live here now, or supabase-js throws "supabaseKey is required"
# at startup.
put PGRST_ANON_KEY "$(mint anon)"

echo "→ host-derived config"
put SITE_URL "$SITE"

echo "→ ports and interface"
# BIND_ADDR is the host interface every published port binds to. 0.0.0.0 is what an unqualified
# "80:80" already meant, so this default changes nothing on an existing box; set it to 127.0.0.1,
# a LAN address or a Tailscale IP to stop publishing on every NIC.
put BIND_ADDR "${BIND_ADDR:-0.0.0.0}"

if [ "$NO_PROXY" = 1 ]; then
  # ── No edge. The app and Keycloak are published directly; nothing terminates TLS. ────────────
  # The overlay is docker-compose.direct.yml; CADDY_REPLICAS=0 is what stops the edge running.
  web_port="${site_port:-8080}"
  # Keycloak needs a SECOND port, because sign-in is a browser redirect to it and there is no
  # proxy left to route /auth. Defaulting to web_port+1 keeps the pair adjacent and predictable;
  # set KEYCLOAK_PORT before running this if that one is taken.
  kc_port="${KEYCLOAK_PORT:-$((web_port + 1))}"
  put CADDY_REPLICAS "0"
  put WEB_PORT       "$web_port"
  put KEYCLOAK_PORT  "$kc_port"
  # PostgREST is read SERVER-SIDE only (web/src/lib/supabase/server.ts), so with no proxy it stays
  # on the compose network rather than needing a third published port.
  put PGRST_URL      "http://postgrest:3000"
  oidc_public="$scheme://$site_host:$kc_port/auth"
else
  # ── Caddy is the edge, which is the default and what partyline.sh's own boxes run. ───────────
  # The port in SITE_URL is the one Caddy publishes for that scheme; the other keeps its default.
  # Moving either of these means Let's Encrypt can no longer validate this box — ACME dials the
  # public 80/443 — so a moved port implies TLS is terminated in front of this stack.
  case "$scheme" in
    https) put HTTP_PORT "${HTTP_PORT:-80}"; put HTTPS_PORT "${site_port:-443}" ;;
    *)     put HTTP_PORT "${site_port:-80}"; put HTTPS_PORT "${HTTPS_PORT:-443}" ;;
  esac
  # Caddy routes /rest/v1/* on this host to PostgREST, so the site URL is also the PostgREST URL.
  put PGRST_URL "$SITE"
  oidc_public="$SITE/auth"
fi

echo "→ relay (shared compose, so the environment-specific bits have to live here)"
# RELAY_API points the relay at its OWN control plane. It defaults to https://partyline.sh in the
# relay binary, so an unset value on staging makes staging's relay heartbeat into prod's pool and
# prod starts handing it real sessions.
put RELAY_API "$SITE"
# RELAY_PORT is the host port the relay publishes. Prod must own :22 — "pppp.sh:22" is compiled into
# every shipped CLI, so a joiner typing `ssh <code>@pppp.sh` cannot reach any other port. Staging has
# no such constraint and stays on 2222, leaving sshd where it is.
# CHANGED FOR SELF-HOST: production takes :22 only because "pppp.sh:22" is compiled into every
# shipped CLI. On your box :22 is sshd, so the relay stays on 2222 and joiners dial it explicitly.
put RELAY_PORT "2222"

echo "→ object storage (MinIO in the stack by default; R2/S3 is a change of the four S3_* values)"
# MinIO's own root credentials. Generated, never shipped: a committed default would be a credential
# in git, and MinIO's built-in minioadmin/minioadmin fallback is exactly what must not happen.
put MINIO_ROOT_USER     "partyline"
put MINIO_ROOT_PASSWORD "$(rnd 32)"

# A box that predates MinIO already has R2_* set. Copy those values across to the S3_* names it now
# reads first — same bytes, same bucket, no flag day — rather than pointing it at an empty MinIO.
# `put` never overwrites, so this is safe to re-run and cannot clobber a deliberate S3_* value.
if have R2_ENDPOINT; then
  echo "  (R2 already configured — carrying it over to the S3_* names)"
  # External storage is already in use, so don't run a storage server this box will never read.
  put MINIO_REPLICAS       "0"
  put S3_ENDPOINT          "$(get R2_ENDPOINT)"
  put S3_BUCKET            "$(get R2_BUCKET)"
  # Plain `if`, not `have X && put Y`: under `set -e` a failing AND-list at the end of a line
  # aborts the whole script, so a box with no R2 key would stop here.
  if have R2_ACCESS_KEY_ID;     then put S3_ACCESS_KEY_ID     "$(get R2_ACCESS_KEY_ID)"; fi
  if have R2_SECRET_ACCESS_KEY; then put S3_SECRET_ACCESS_KEY "$(get R2_SECRET_ACCESS_KEY)"; fi
else
  # Fresh box: the stack's own MinIO, reachable only on the compose network. The bucket is created
  # on first `docker compose up` by the minio-init service.
  put S3_ENDPOINT          "http://minio:9000"
  put S3_BUCKET            "partyline"
  put S3_ACCESS_KEY_ID     "$(get MINIO_ROOT_USER)"
  put S3_SECRET_ACCESS_KEY "$(get MINIO_ROOT_PASSWORD)"
fi

chmod 600 .env

# ── The bundled identity provider ──────────────────────────────────────────────────────────────
#
# partyline has no local accounts: sign-in is OIDC, and it is the only sign-in there is. Keycloak
# ships in the stack and everything it needs is generated here, so nobody has to read Keycloak's
# documentation to log into partyline once.
put KEYCLOAK_ADMIN_USER "admin"
put KEYCLOAK_ADMIN_PASSWORD "$(rnd 24)"
put OIDC_CLIENT_ID "partyline-web"
put OIDC_CLIENT_SECRET "$(rnd 32)"
# THE THREE URLS THAT MUST MATCH EXACTLY.
#   OIDC_PUBLIC_URL  what a browser reaches Keycloak on — compose passes it as KC_HOSTNAME
#   OIDC_ISSUER      what the app discovers, and what Keycloak stamps into every token's `iss`
#   the realm path   /realms/partyline under both
# They are all built from $oidc_public, computed once above, so they cannot drift: behind Caddy
# that is SITE_URL/auth, and with --no-proxy it is Keycloak's own published port. A mismatch of a
# single character — a port, a trailing slash, http vs https — makes discovery refuse the document
# and sign-in fails with nothing useful in any log.
put OIDC_PUBLIC_URL "$oidc_public"
put OIDC_ISSUER     "$oidc_public/realms/partyline"
# PLAIN HTTP IS A DELIBERATE DOWNGRADE, AND THE APP MAKES YOU SAY SO. web/src/lib/api/oidc.ts
# refuses a non-https issuer unless this is set, because over http the discovery document and the
# JWKS can be rewritten in flight and a rewritten JWKS verifies a forged token perfectly. On a
# private network that attacker does not exist; on the internet they do. Loopback never needs it.
case "$scheme://$site_host" in
  http://localhost|http://127.0.0.1|http://\[::1\]|http://::1) ;;
  http://*) put OIDC_ALLOW_INSECURE_ISSUER "1" ;;
esac

# THE REALM, WRITTEN NOT CLICKED. Keycloak imports this once, on an empty data directory, exactly
# as Postgres runs 00-bootstrap.sh — so a fresh install has a working client and a first account
# without anyone opening an admin console.
#
# Only written if absent: re-running this script must never overwrite a realm whose users and
# federation somebody has since configured. Keycloak ignores the import once the realm exists, but
# clobbering the file would still lose the record of what was imported.
# THE ONE SETTING THAT DECIDES WHETHER AN http:// INSTALL CAN SIGN IN AT ALL. This was the
# literal "external", which means "require TLS for everything except a loopback address" — so on
# an http://<lan-ip>:<port> box Keycloak refuses every request with HTTPS_REQUIRED and the install
# is unusable, with no way to change it short of the admin console it will not let you reach.
# Derive it from the scheme instead: "none" for http, "external" for https, which is byte-identical
# to what every existing box already has.
if [ "$scheme" = "http" ]; then SSL_REQUIRED="none"; else SSL_REQUIRED="external"; fi
FIRST_USER="${FIRST_USER:-admin@$(printf '%s' "$SITE" | sed -e 's#^[a-zA-Z][a-zA-Z0-9+.-]*://##' -e 's#[:/].*##')}"
if [ ! -f keycloak/realm-partyline.json ]; then
  mkdir -p keycloak
  FIRST_PASSWORD="$(rnd 20)"
  cat > keycloak/realm-partyline.json <<REALM
{
  "realm": "partyline",
  "enabled": true,
  "sslRequired": "$SSL_REQUIRED",
  "registrationAllowed": false,
  "clients": [
    {
      "clientId": "$(grep '^OIDC_CLIENT_ID=' .env | cut -d= -f2-)",
      "enabled": true,
      "protocol": "openid-connect",
      "publicClient": false,
      "secret": "$(grep '^OIDC_CLIENT_SECRET=' .env | cut -d= -f2-)",
      "standardFlowEnabled": true,
      "directAccessGrantsEnabled": false,
      "redirectUris": ["$SITE/api/auth/callback"],
      "webOrigins": ["$SITE"]
    }
  ],
  "users": [
    {
      "username": "$FIRST_USER",
      "email": "$FIRST_USER",
      "emailVerified": true,
      "enabled": true,
      "credentials": [{ "type": "password", "value": "$FIRST_PASSWORD", "temporary": true }]
    }
  ]
}
REALM
  chmod 600 keycloak/realm-partyline.json
  echo
  echo "IDENTITY — written to keycloak/realm-partyline.json, imported on first start."
  echo "  Sign in at $SITE with:"
  echo "    $FIRST_USER"
  echo "    $FIRST_PASSWORD"
  echo "  You will be asked to change that password immediately (it is marked temporary)."
  echo "  THIS IS PRINTED ONCE. It is not stored anywhere you can read it back."
fi

echo
echo "HOW TO BRING IT UP"
if [ "$NO_PROXY" = 1 ]; then
  echo "  No reverse proxy, no TLS. Use BOTH compose files, every time:"
  echo "    cd $install_dir && docker compose -f docker-compose.yml -f docker-compose.direct.yml up -d"
  echo "  The app answers on $SITE and Keycloak on $oidc_public."
  echo "  Both are PLAIN HTTP. Keep this on a private network (LAN, VPN, Tailscale) — anyone who"
  echo "  can see the wire sees session cookies. Do not expose it to the internet."
else
  echo "    cd $install_dir && docker compose up -d"
  echo "  Caddy is the edge; put your hostname in the Caddyfile first."
fi
echo "  Ports are set in .env (BIND_ADDR, and HTTP_PORT/HTTPS_PORT or WEB_PORT/KEYCLOAK_PORT)."
echo
echo "STILL MISSING — real credentials. Append them to .env by hand; it is chmod 600:"
missing=0
for v in S3_ACCESS_KEY_ID S3_SECRET_ACCESS_KEY RESEND_API_KEY; do
  have "$v" || { echo "  ! $v"; missing=1; }
done
[ "$missing" = 0 ] && echo "  (none — everything required is set)"
echo
# SIGN-IN IS ALREADY DONE, AND SAYING SO MATTERS. It used to list a commercial provider's keys
# under "still missing", which reads as mandatory — so a self-hoster concluded partyline required an
# account somewhere. It does not: OIDC is the only provider, and this script configured it.
echo "SIGN-IN — already configured. Keycloak ships in the stack and this script wrote its realm,"
echo "its client secret and your first account. Nothing further is required to log in."
echo "  * To use your own provider instead (Okta, Entra, Authentik, your corporate IdP), override"
echo "    OIDC_ISSUER / OIDC_CLIENT_ID / OIDC_CLIENT_SECRET in .env and drop the keycloak service."
echo "  * To federate rather than replace — GitHub, Google, LDAP — add it in the Keycloak console"
echo "    at \$SITE/auth, signed in with KEYCLOAK_ADMIN_USER and the password printed above."
echo
echo "optional (feature stays dark until set): RESEND_API_KEY ·"
echo "GITHUB_APP_* · SLACK_CLIENT_ID/SECRET/SIGNING_SECRET · DISCORD_BOT_TOKEN"
