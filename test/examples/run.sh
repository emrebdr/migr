#!/usr/bin/env bash
# Runs every project in examples/ the way its README does (M14-T6): each against its own compose.yaml,
# with this commit's migr first on PATH, every exit code checked. `make test-examples` builds migr and
# the TypeScript SDK first.
#
#   EXAMPLES   the examples to run, default all four
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
export PATH="$root/bin:$PATH"
export MIGR_BIN="$root/bin/migr"
unset DATABASE_URL MIGR_RELEASE MIGR_UNATTENDED

failures=0
work=$(mktemp -d)
running=""
trap 'for dir in $running; do (cd "$dir" && docker compose down -v >/dev/null 2>&1) || true; done; rm -rf "$work"' EXIT

fail() { echo "FAIL: $*"; failures=$((failures + 1)); }
pass() { echo "ok:   $*"; }

# expect runs one command in an example and checks its exit status, printing its output on a mismatch.
expect() {
  local want=$1 what=$2
  shift 2
  local got=0
  "$@" >"$work/out" 2>&1 || got=$?
  if [ "$got" -eq "$want" ]; then pass "$what (exit $got)"; else fail "$what: exit $got, want $want"; sed 's/^/      /' "$work/out"; fi
}

# says checks the output of the last command run through expect holds a phrase.
says() {
  if grep -qF -- "$2" "$work/out"; then pass "$1"; else fail "$1: no \"$2\" in the output"; sed 's/^/      /' "$work/out"; fi
}

# database starts the example's own compose project and waits for it to be healthy.
database() {
  docker compose up -d --wait >/dev/null 2>&1
  running="$running $PWD"
}

# release stops the example's compose project and deletes its volumes.
release() {
  docker compose down -v >/dev/null 2>&1
  running=${running/" $PWD"/}
}

mixed_sqlite() {
  export DATABASE_URL="$work/mixed.db"
  expect 0 "mixed-sqlite: npm ci" npm ci --no-audit --no-fund
  expect 0 "mixed-sqlite: go vet" go vet ./...
  expect 0 "mixed-sqlite: plan" migr plan
  says "mixed-sqlite: the foreign key is a table rebuild" "emulated: table rebuild"
  expect 0 "mixed-sqlite: validate" migr validate
  expect 0 "mixed-sqlite: up" migr up
  says "mixed-sqlite: all three languages ran" "applied 3 migrations"
  expect 0 "mixed-sqlite: down" migr down --step 3
  says "mixed-sqlite: all three rolled back" "rolled back 3 migrations"
  unset DATABASE_URL
}

go_postgres() {
  expect 0 "go-postgres: go vet" go vet ./...
  expect 0 "go-postgres: go test" go test ./...
  database
  expect 0 "go-postgres: validate --require-safe" migr validate --require-safe
  expect 0 "go-postgres: plan --safety --to" migr plan --safety --to 20261101T100100
  says "go-postgres: the index is built concurrently" "CREATE INDEX CONCURRENTLY"
  expect 0 "go-postgres: up" migr up
  says "go-postgres: all three applied" "applied 3 migrations"
  expect 1 "go-postgres: down refuses the irreversible tier" migr down
  says "go-postgres: with the migration's own reason" "cannot be undone"
  release
}

ts_mysql() {
  expect 0 "ts-mysql: npm ci" npm ci --no-audit --no-fund
  expect 0 "ts-mysql: npm test" npm test
  database
  expect 5 "ts-mysql: validate --require-safe declares add_column at its worst" migr validate --require-safe
  expect 0 "ts-mysql: validate --connect" migr validate --connect
  expect 0 "ts-mysql: plan --safety" migr plan --safety
  says "ts-mysql: the backfill costs one chunk" "brief_exclusive (declared table_rewrite, one chunk at a time)"
  expect 0 "ts-mysql: up" migr up
  says "ts-mysql: the backfill ran in chunks" "1 chunk"
  expect 0 "ts-mysql: down" migr down --step 3
  says "ts-mysql: all three rolled back" "rolled back 3 migrations"
  release
}

zero_downtime() {
  expect 0 "zero-downtime: go vet" go vet ./...
  database
  expect 0 "zero-downtime: walkthrough.sh" ./walkthrough.sh
  says "zero-downtime: every app version served without a failed query" "with no failed query"
  says "zero-downtime: each early step was held" "⏸ held"
  release
}

for example in ${EXAMPLES:-mixed-sqlite go-postgres ts-mysql zero-downtime}; do
  echo "== $example"
  cd "$root/examples/$example"
  "${example//-/_}"
done
cd "$root"

if [ "$failures" -ne 0 ]; then
  echo "$failures check(s) failed"
  exit 1
fi
echo "every example ran as its README says"
