#!/usr/bin/env bash
# Harness for deploy/stack/apply-migrations.sh — proves the ledger-read guards behave, including a
# reproduction of the exact prod failure (bulk read missing an APPLIED old migration).
#
# The real script talks to postgres via a `psql()` function; this harness rewrites that one
# function to serve canned answers from a fake ledger, leaving every line of decision logic
# untouched and executed for real.
set -u

SCRIPT="$(cd "$(dirname "$0")" && pwd)/apply-migrations.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ---- build a testable copy: swap ONLY the docker-backed call sites ---------------------------
make_sut() { # $1 = dir
  mkdir -p "$1/migrations"
  SUT_OUT="$1/apply.sh" python3 - "$SCRIPT" <<'PY'
import os, re, sys
src = open(sys.argv[1]).read()
# the psql() wrapper → fake
src = re.sub(r'^psql\(\) \{ docker compose exec .*$',
             'psql() { fake_psql "$@"; }', src, count=1, flags=re.M)
# the two-line single-transaction apply pipeline → fake_apply (still fed by the same { cat; printf } stdin)
src = re.sub(r'\} \| docker compose exec -T postgres \\\n\s*psql -U postgres -d partyline -v ON_ERROR_STOP=1 --single-transaction -q -f -',
             '} | fake_apply', src, count=1)
src = src.replace('cd /opt/partyline', 'cd "$SUT_DIR"')
live = [l for l in src.splitlines() if 'docker compose' in l and not l.lstrip().startswith('#')]
assert not live, f"a docker call survived the rewrite: {live}"
open(os.environ['SUT_OUT'], 'w').write(src)
PY
  chmod +x "$1/apply.sh"
}

# The fake psql: answers from $LEDGER_FILE (one version per line). Failure modes are switched by
# env vars so each scenario is one line to set up.
fake_env() { # $1 = dir; writes a prelude sourced by the SUT via BASH_ENV
  cat > "$1/fakes.sh" <<'FAKES'
fake_psql() {
  # find the SQL among the args (last non-flag arg after -tAc / -c)
  local sql=""
  local prev=""
  for a in "$@"; do
    case "$prev" in -tAc|-c) sql="$a";; esac
    prev="$a"
  done
  case "$sql" in
    "create table if not exists schema_migrations"*) return 0 ;;
    "select count(*) from schema_migrations;")
      if [ "${FAKE_COUNT_ERROR:-}" = "1" ]; then return 2; fi
      wc -l < "$LEDGER_FILE" | tr -d ' '
      ;;
    "select version from schema_migrations order by version;")
      # The transient under test: serve a TRUNCATED list when asked.
      if [ -n "${FAKE_TRUNCATE_BULK:-}" ]; then
        head -n "$FAKE_TRUNCATE_BULK" "$LEDGER_FILE"
      else
        sort "$LEDGER_FILE"
      fi
      ;;
    "select version from schema_migrations;")
      if [ -n "${FAKE_TRUNCATE_BULK:-}" ]; then
        head -n "$FAKE_TRUNCATE_BULK" "$LEDGER_FILE"
      else
        sort "$LEDGER_FILE"
      fi
      ;;
    "select count(*) from schema_migrations where version = "*)
      local v="${sql#*version = \'}"; v="${v%\';}"
      if [ "${FAKE_RECHECK_GARBLED:-}" = "1" ]; then echo "ERROR: server closed"; return 0; fi
      grep -cxF "$v" "$LEDGER_FILE" || true
      ;;
    "notify pgrst, 'reload schema';") return 0 ;;
    "select count(*) from schema_migrations;"*) wc -l < "$LEDGER_FILE" ;;
    *) echo "fake_psql: unhandled SQL: $sql" >&2; return 3 ;;
  esac
}
fake_apply() {
  # stands in for the --single-transaction apply+record; reads stdin like the real one
  local body; body="$(cat)"
  local v; v="$(printf '%s' "$body" | sed -n "s/.*values ('\(.*\)') on conflict.*/\1/p")"
  # replaying an applied migration fails, like the real create-table replay does
  if grep -qxF "$v" "$LEDGER_FILE"; then
    echo "psql: ERROR: relation already exists (replayed $v)" >&2
    return 3
  fi
  echo "$v" >> "$LEDGER_FILE"
}
FAKES
}

run_case() { # $1 name, $2 expected-exit, then env assignments as "K=V"...
  local name="$1" want="$2"; shift 2
  local dir="$WORK/$name"
  make_sut "$dir"
  fake_env "$dir"
  local out rc
  out="$(cd "$dir" && env "$@" SUT_DIR="$dir" LEDGER_FILE="$dir/ledger" \
        bash -c 'source ./fakes.sh; export -f fake_psql fake_apply; source ./apply.sh' 2>&1)"
  rc=$?
  if [ "$rc" -eq "$want" ]; then
    echo "PASS  $name (exit $rc)"
  else
    echo "FAIL  $name — wanted exit $want, got $rc"
    echo "$out" | sed 's/^/      /'
    FAILED=1
  fi
  LAST_OUT="$out"
}

FAILED=0

# Fixture: 5 applied migrations, all present on disk.
setup5() { # $1 dir
  mkdir -p "$1"
  : > "$1/ledger"
  for i in 1 2 3 4 5; do
    echo "000${i}_m.sql" >> "$1/ledger"
    mkdir -p "$1/migrations"; echo "select 1;" > "$1/migrations/000${i}_m.sql"
  done
}

# 1. Healthy incremental deploy: everything applied → applies 0, exits 0.
setup5 "$WORK/ok"; run_case ok 0
echo "$LAST_OUT" | grep -q "applied 0 this run" || { echo "FAIL  ok: wrong summary"; FAILED=1; }

# 2. THE 22:32 FAILURE: bulk read truncated (3 of 5 rows) so 0004+0005 look unapplied — but the
#    ledger has them. Old behavior: replay 0004 → fatal. New behavior: re-check catches it, warns,
#    skips, deploy exits 0.
setup5 "$WORK/truncated"; run_case truncated 0 FAKE_TRUNCATE_BULK=3
echo "$LAST_OUT" | grep -q "Distrusting the bulk read" \
  || { echo "FAIL  truncated: expected the distrust fallback"; echo "$LAST_OUT" | sed 's/^/      /'; FAILED=1; }
echo "$LAST_OUT" | grep -q "bulk read missed it but the ledger has it" \
  || { echo "FAIL  truncated: expected per-file skip warnings"; FAILED=1; }
echo "$LAST_OUT" | grep -q "applied 0 this run" || { echo "FAIL  truncated: applied something"; FAILED=1; }
echo "$LAST_OUT" | grep -q "replayed" && { echo "FAIL  truncated: A REPLAY HAPPENED"; FAILED=1; }

# 3. Count/list disagreement (count errors → count '', list 5) → loud abort, no decisions made.
setup5 "$WORK/badcount"; run_case badcount 1 FAKE_COUNT_ERROR=1
echo "$LAST_OUT" | grep -q "could not read the migration ledger count" || { echo "FAIL  badcount: wrong message"; FAILED=1; }

# 4. Genuinely new migration → applied exactly once, recorded.
setup5 "$WORK/new"; echo "select 1;" > "$WORK/new/migrations/0006_new.sql"
run_case new 0
echo "$LAST_OUT" | grep -q "applied 1 this run" || { echo "FAIL  new: did not apply the new one"; FAILED=1; }
grep -qxF "0006_new.sql" "$WORK/new/ledger" || { echo "FAIL  new: not recorded"; FAILED=1; }

# 5. Re-check itself garbled during a truncation event → FATAL, never "not recorded → replay".
setup5 "$WORK/garbled"; run_case garbled 1 FAKE_TRUNCATE_BULK=3 FAKE_RECHECK_GARBLED=1
echo "$LAST_OUT" | grep -q "Refusing to guess" || { echo "FAIL  garbled: fell through fail-open"; FAILED=1; }
echo "$LAST_OUT" | grep -q "replayed" && { echo "FAIL  garbled: A REPLAY HAPPENED"; FAILED=1; }

echo
[ "$FAILED" -eq 0 ] && echo "ALL SCENARIOS PASS" || { echo "FAILURES"; exit 1; }
