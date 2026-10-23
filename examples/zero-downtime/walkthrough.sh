#!/usr/bin/env bash
# Replaces users.email with users.email_address over five deploys while the app keeps serving.
#
# Each deploy runs `migr up --release dN` while the previous app version is serving, then rolls the
# app forward, starting the new version before stopping the old one, as a rolling deploy does. The
# contract is the exception: it runs after the last app version is serving, because the version
# before it still writes the old column. Every app version counts its failed queries, and this
# script fails if there was one.
#
# It needs an empty database: `docker compose down -v && docker compose up -d --wait` gives one.
set -euo pipefail

cd "$(dirname "$0")"
export DATABASE_URL=${DATABASE_URL:-postgres://app:app@localhost:55433/app?sslmode=disable}

serving=""
failed=0

# say prints one step of the walkthrough as a heading.
say() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

# start runs one app version in the background and records it as serving.
start() {
  .bin/app -version "$1" >".bin/app-v$1.log" 2>&1 &
  serving="$serving $!:$1"
  sleep 1
}

# stop ends the oldest serving app version and adds its result to the walkthrough's.
stop() {
  local first=${serving# }
  first=${first%% *}
  serving=${serving#*"$first"}
  local pid=${first%%:*} version=${first##*:}
  kill -TERM "$pid"
  if ! wait "$pid"; then failed=$((failed + 1)); fi
  cat ".bin/app-v$version.log"
}

# roll starts the next app version and then stops the one serving before it.
roll() {
  say "roll the app out: v$1"
  start "$1"
  stop
}

# held runs migr up and checks it applied one migration and held at the next step.
held() {
  local out
  out=$(migr up --release "$1")
  printf '%s\n' "$out"
  if ! grep -q '⏸ held' <<<"$out"; then
    echo "walkthrough: migr up --release $1 was expected to hold at the next step" >&2
    exit 1
  fi
}

trap 'for app in $serving; do kill -TERM "${app%%:*}" 2>/dev/null || true; done' EXIT

if [ "$(migr status --json | grep -c '"state": *"applied"' || true)" -ne 0 ]; then
  echo "walkthrough: the database already holds applied migrations; start from an empty one" >&2
  exit 1
fi

go build -o .bin/app ./app

say "the gate CI runs on every pull request"
migr validate --require-safe

say "deploy 0: the app as it is, serving from users.email"
migr up --to 20261101T120000 --release d0
start 0

say "deploy 1: expand"
held d1
roll 1

say "deploy 2: backfill"
held d2
roll 2

say "deploy 3: swap"
held d3
roll 3

say "deploy 4: the app first, then contract"
roll 4
migr up --release d4

say "done"
stop
migr status

if [ "$failed" -ne 0 ]; then
  echo "walkthrough: $failed app version(s) saw failed queries" >&2
  exit 1
fi
echo "every app version served through every migration with no failed query"
