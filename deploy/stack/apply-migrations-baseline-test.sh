#!/usr/bin/env bash
# Exercises ONLY the baseline branch of apply-migrations.sh, with psql stubbed.
# The danger in that block is which PATH it takes and what it records — not the SQL text.
set -uo pipefail

run_case() {
  local name="$1" schema="$2" expect="$3"
  rm -rf work && mkdir -p work/migrations && cd work
  printf '# a comment\n\n0001_core.sql\n0017_uses_personal.sql\nmissing_file.sql\n' > migrations/BASELINE
  : > migrations/0001_core.sql
  : > migrations/0017_uses_personal.sql
  : > recorded.log

  # Stub: to_regclass answers per case; the insert logs and reports a new row.
  psql() {
    local q="${*}"
    if [[ "$q" == *"to_regclass"* ]]; then echo "$schema"; return 0; fi
    if [[ "$q" == *"insert into schema_migrations"* ]]; then
      echo "$q" | grep -o "values ('[^']*')" | sed "s/values ('//;s/')//" >> recorded.log
      echo "1"; return 0
    fi
    return 0
  }

  # The block under test, copied verbatim from the script.
  out="$(
    if [ -f migrations/BASELINE ]; then
      has_schema="$(psql -tAc "select to_regclass('public.orgs') is not null;" | tr -d '[:space:]')"
      if [ "$has_schema" = "t" ]; then
        recorded=0
        while IFS= read -r v; do
          case "$v" in ''|\#*) continue ;; esac
          [ -f "migrations/$v" ] || continue
          n="$(psql -tAc "insert into schema_migrations (version) values ('$v') on conflict (version) do nothing returning 1;" | tr -d '[:space:]')"
          [ -n "$n" ] && recorded=$((recorded + 1))
        done < migrations/BASELINE
        echo "baseline: recorded $recorded historical migration(s) without executing them"
      else
        echo "baseline: fresh database — historical migrations will be APPLIED normally, not skipped"
      fi
    fi
  )"

  local got; got="$(cat recorded.log | tr '\n' ',')"
  if [[ "$out" == *"$expect"* ]]; then echo "PASS  $name"; else echo "FAIL  $name"; echo "      got: $out"; fi
  echo "      recorded: ${got:-<none>}"
  cd ..
}

run_case "fresh database RUNS everything (records nothing)" "f" "fresh database"
run_case "existing schema RECORDS without executing"        "t" "recorded 2 historical"
