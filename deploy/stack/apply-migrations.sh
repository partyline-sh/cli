#!/usr/bin/env bash
# Apply every partyline migration to the box's own Postgres with plain psql.
#
# This IS the portability gate (task #177): no Supabase CLI, no --project-ref (which cannot work
# off-platform), no vendor tooling. If this script runs clean, the schema is portable.
#
# WHY THIS IS A FILE AND NOT A HEREDOC IN THE WORKFLOW:
# it used to be `ssh box 'bash -s' <<'REMOTE' … REMOTE`, which puts the script on ssh's STDIN.
# Every psql call here is `docker compose exec -T`, and -T READS STDIN — so the first `psql -c`
# consumed the remainder of the script. The create-table ran, everything after it silently
# vanished, bash hit EOF, and the step exited 0. Three deploys reported success against a database
# with one table and zero migrations. Shipping the script as a file removes the whole class of bug.
#
# Idempotency comes from schema_migrations — the same contract goose or atlas would give, without
# adding a tool before we know we need one.
set -euo pipefail
# RUN WHERE YOU LIVE, not where our box happens to put you. This was `cd /opt/partyline`, which is
# right for partyline.sh's own machines and wrong for everyone else: the published copy is byte-
# identical, so a self-hoster who installed anywhere else had the script silently jump to a
# directory that does not exist and exit under `set -e`.
cd "$(cd "$(dirname "$0")" && pwd)"

# </dev/null on every call: belt and braces. Nothing here should read stdin, and if a future edit
# adds something that does, it gets EOF instead of eating the script.
psql() { docker compose exec -T postgres psql -U postgres -d partyline -v ON_ERROR_STOP=1 "$@" </dev/null; }

psql -c "create table if not exists schema_migrations (
           version    text primary key,
           applied_at timestamptz not null default now()
         );" >/dev/null

# ── BASELINE: history that must be RECORDED, not RUN ─────────────────────────────────────────────
#
# A migration is only replayable until a LATER one changes what it depends on. 0017 joins on
# `orgs.personal`; 20260728070000_one_org_per_user.sql dropped that column, so 0017 can never run
# again — a permanent failure, not a retryable one.
#
# That is harmless while the ledger is complete. It stops being harmless when a row is MISSING, and
# rows can be missing: before apply+record were made atomic (see the loop below) an interrupted
# deploy could leave a migration applied but unrecorded. That fix prevents NEW gaps and cannot
# repair OLD ones. Staging carried such a scar for 0017 and it halted deploys twice on 2026-08-01.
#
# THE CONDITION IS THE WHOLE SAFETY. Recording-without-running is only correct when the schema is
# ALREADY THERE. On a genuinely fresh database these files must still execute, or a new environment
# comes up with a complete history and no tables — a far worse failure than the one being fixed, and
# a silent one. `orgs` is the probe: it has existed since 0001 and no migration since has dropped it.
if [ -f migrations/BASELINE ]; then
  has_schema="$(psql -tAc "select to_regclass('public.orgs') is not null;" | tr -d '[:space:]')"
  # THE TWO SIGNALS MUST AGREE. `orgs` is one table's name; if it is ever renamed the probe silently
  # reports "fresh" and this script would RUN 59 historical migrations against a live schema — a far
  # worse failure than the one being fixed, and a silent one. A database with recorded history is
  # not fresh by any definition, so disagreement means something is wrong that must not be papered
  # over by guessing. Fail loudly instead: a halted deploy is recoverable, a replayed history is not.
  ledger_rows="$(psql -tAc "select count(*) from schema_migrations;" | tr -d '[:space:]')"
  if [ "$has_schema" != "t" ] && [ "${ledger_rows:-0}" -gt 0 ]; then
    echo "FATAL: schema_migrations has $ledger_rows row(s) but the schema probe says this database is empty."
    echo "Those cannot both be true. Either the probe table was renamed (fix the probe) or this"
    echo "database is not what it claims to be. Refusing to run historical migrations against it."
    exit 1
  fi

  if [ "$has_schema" = "t" ]; then
    recorded=0
    while IFS= read -r v; do
      case "$v" in ''|\#*) continue ;; esac
      [ -f "migrations/$v" ] || continue   # listed but not shipped — ignore rather than invent a row
      # COUNT THE INSERT IN SQL, not by testing psql's output for emptiness. `-tAc` still prints the
      # command tag ("INSERT 0 0") when a RETURNING clause matches no rows, so a no-op read as a
      # success and this counter reported every listed migration as recorded — 59 every time, even
      # on a run that inserted nothing. A wrapping CTE returns a plain 0 or 1 with no tag to confuse.
      n="$(psql -tAc "with ins as (
                        insert into schema_migrations (version) values ('$v')
                        on conflict (version) do nothing returning 1
                      ) select count(*) from ins;" | tr -d '[:space:]')"
      [ "$n" = "1" ] && recorded=$((recorded + 1))
    done < migrations/BASELINE
    # Say what happened. A step that quietly rewrites the ledger is how the incident this fixes
    # became impossible to explain after the fact.
    echo "baseline: recorded $recorded historical migration(s) without executing them"
  else
    echo "baseline: fresh database — historical migrations will be APPLIED normally, not skipped"
  fi
fi

shopt -s nullglob
files=(migrations/*.sql)
if [ ${#files[@]} -eq 0 ]; then
  echo "FATAL: no migration files at $(pwd)/migrations — the rsync step shipped nothing."
  echo "A migration step that 'passes' by finding zero files is how an empty database reaches"
  echo "production looking green. Failing instead."
  exit 1
fi
echo "found ${#files[@]} migration files"

# THE READ IS AS LOAD-BEARING AS THE WRITE, and it has now lied twice in one day. The write side
# is atomic (migration + ledger row, one transaction, below) — but the SKIP decision trusts this
# one bulk SELECT absolutely. Any version missing from its output is replayed, and replaying an
# already-applied migration against today's schema is fatal (staging replayed
# 20260709200000_party_activity; prod replayed 0017 against a schema where orgs.personal no
# longer exists — while reconcile showed the ledger complete both times: 143 recorded, 143 files,
# zero drift). So the ledger was fine and the READ was wrong: a short or garbled result stream
# turned "connection hiccup" into "replay old migrations", silently.
#
# Two guards, because the exact transient could not be reproduced and a fix aimed at a guess
# would just move the hole:
#
#   1. The bulk read is VERIFIED: its line count must equal select count(*). A truncated stream
#      cannot match, and a mismatch is a loud abort — never a replay.
#   2. Replay requires a SECOND OPINION: before any file is applied, the ledger is asked directly
#      about that one version. Only "not recorded", from a fresh single-row query, replays it.
#      A one-line response has no meaningful truncation, and an error aborts via ON_ERROR_STOP +
#      set -e. The bulk list is now only an optimization for the common all-applied case.
applied_count="$(psql -tAc "select count(*) from schema_migrations;" | tr -d '[:space:]')" \
  || { echo "FATAL: could not read the migration ledger count."; exit 1; }
case "$applied_count" in
  '' | *[!0-9]*)
    echo "FATAL: ledger count read returned '$applied_count', not a number. Refusing to guess."
    exit 1
    ;;
esac
applied="$(psql -tAc "select version from schema_migrations order by version;")" \
  || { echo "FATAL: could not read the migration ledger list."; exit 1; }
listed="$(printf '%s' "$applied" | grep -c . || true)"
if [ "$listed" -ne "$applied_count" ]; then
  # The bulk read is lying — this is the moment that used to become a fatal replay. Don't trust
  # it, and don't fail the deploy either: fall back to asking the ledger about EVERY file
  # individually (recorded(), below). Slower, self-healing, and impossible to replay from.
  echo "⚠ ledger read inconsistent — count(*) says $applied_count rows, the list read $listed."
  echo "  Distrusting the bulk read; every file will be checked against the ledger individually."
  applied=""
fi
echo "ledger: $applied_count recorded"

recorded() { # is this ONE version in the ledger, asked directly — the gate replay must pass
  local n
  n="$(psql -tAc "select count(*) from schema_migrations where version = '$1';" | tr -d '[:space:]')" || n=""
  case "$n" in
    1) return 0 ;;
    0) return 1 ;;
    *)
      # A failed or garbled check must ABORT, not fall through to "not recorded" — treating an
      # error as permission to replay is the exact fail-open this function exists to close.
      echo "FATAL: ledger check for $1 returned '$n' (expected 0 or 1). Refusing to guess."
      exit 1
      ;;
  esac
}

count=0
for f in "${files[@]}"; do
  v="$(basename "$f")"
  if printf '%s\n' "$applied" | grep -qxF "$v"; then continue; fi
  if recorded "$v"; then
    # The bulk read missed a version the ledger has. This is the failure that used to become a
    # fatal replay; now it is a line in a log. Loud on purpose — it means the transient is still
    # out there, just harmless.
    echo "⚠ $v: bulk read missed it but the ledger has it — skipping, NOT replaying"
    continue
  fi
  echo "→ $v"
  # THE MIGRATION AND ITS LEDGER ROW GO IN ONE TRANSACTION. This used to be two psql calls — apply,
  # then record — on two connections and two commits. Anything that stopped the process in between
  # (a cancelled job, a dropped ssh, an OOM, a box reboot) left the migration APPLIED BUT UNRECORDED,
  # and the next deploy replayed it. For a migration with a bare `create table` that replay is fatal,
  # so one interrupted deploy wedges every deploy after it. That is exactly how staging broke on
  # 0051_daemon_project_assignments: the table existed, the ledger did not know, and every subsequent
  # deploy died on the replay.
  #
  # Concatenating the INSERT onto the migration and feeding both to ONE --single-transaction psql
  # makes the two atomic: either the schema change and its record both land, or neither does. The
  # ledger can no longer disagree with the database, whatever kills the process.
  #
  # ON CONFLICT is kept: the applied list is read ONCE before the loop, so a concurrent run or a
  # hand-applied migration could still have recorded this version since. A duplicate key must not
  # abort the batch part-way through — that is the worst possible place to stop.
  {
    cat "$f"
    printf "\ninsert into schema_migrations (version) values ('%s') on conflict (version) do nothing;\n" "$v"
  } | docker compose exec -T postgres \
        psql -U postgres -d partyline -v ON_ERROR_STOP=1 --single-transaction -q -f -
  count=$((count + 1))
done

# PostgREST caches the schema at BOOT. A migration that adds a table, column or function is
# invisible to it until the cache reloads — the API keeps answering, but with the OLD schema, so
# the failure looks like "Could not find the function … in the schema cache" long after the
# migration succeeded. This bit us on the very first live sign-in.
#
# NOTIFY is the documented way to reload in place: no restart, no dropped connections, and it is
# harmless when nothing changed. It belongs HERE rather than in a deploy step, so that anyone
# applying migrations by hand gets it too.
psql -c "notify pgrst, 'reload schema';" >/dev/null
echo "postgrest schema cache reloaded"

total="$(psql -tAc "select count(*) from schema_migrations;" | tr -d '[:space:]')"
echo "applied $count this run · $total recorded total"

# A run that applied nothing AND has nothing recorded means the loop never did any work — the same
# silent-success failure in a different disguise. Refuse to call that a success.
if [ "$total" -eq 0 ]; then
  echo "FATAL: zero migrations recorded after processing ${#files[@]} files."
  exit 1
fi
