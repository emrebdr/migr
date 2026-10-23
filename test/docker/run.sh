#!/usr/bin/env bash
# Runs the two release images the way a pipeline does (M14-T4): a project mounted at /work,
# the DSN in the environment, the image's own non-root user. `make test-docker` builds the images and
# starts compose's postgres17 first.
#
#   MIGR_IMAGE         the minimal image, e.g. migr:test
#   MIGR_NODE_IMAGE    the node image, e.g. migr:test-node
#   MIGR_VERSION       the version both binaries must report
#   COMPOSE            the compose file postgres17 is running from
#   SDK_DIR            sdk/typescript, already built
set -euo pipefail

: "${MIGR_IMAGE:?}" "${MIGR_NODE_IMAGE:?}" "${MIGR_VERSION:?}" "${COMPOSE:?}" "${SDK_DIR:?}"

failures=0
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
chmod 755 "$work"

container=$(docker compose -f "$COMPOSE" ps -q postgres17)
network=$(docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}' "$container")
dsn="postgres://migr:migr@postgres17:5432/migr?sslmode=disable"

fail() { echo "FAIL: $*"; failures=$((failures + 1)); }
pass() { echo "ok:   $*"; }

# migr runs one command in an image against a project, printing its output and returning its exit status.
migr() {
  local image=$1 project=$2
  shift 2
  docker run --rm --network "$network" -e DATABASE_URL="$dsn" -v "$project:/work" "$image" "$@"
}

# expect runs a migr command and checks its exit status.
expect() {
  local want=$1 what=$2
  shift 2
  local got=0
  migr "$@" >"$work/out" 2>&1 || got=$?
  if [ "$got" -eq "$want" ]; then pass "$what (exit $got)"; else fail "$what: exit $got, want $want"; sed 's/^/      /' "$work/out"; fi
}

# project writes a migr.yaml for one language and history table into a fresh directory.
project() {
  local dir="$work/$1" language=$2 table=$3
  mkdir -p "$dir/migrations"
  cat >"$dir/migr.yaml" <<YAML
version: 1
migrations:
  dir: ./migrations
  language: $language
databases:
  default:
    driver: postgres
    dsn: \${DATABASE_URL}
history:
  table: $table
validation:
  allow_destructive: [raw, drop_table, alter_column]
YAML
  echo "$dir"
}

for image in "$MIGR_IMAGE" "$MIGR_NODE_IMAGE"; do
  user=$(docker image inspect -f '{{.Config.User}}' "$image")
  if [ -n "$user" ] && [ "$user" != root ] && [ "$user" != 0 ]; then pass "$image runs as $user"; else fail "$image runs as root"; fi

  reported=$(docker run --rm "$image" version --json | sed -n 's/.*"version": "\([^"]*\)".*/\1/p')
  if [ "$reported" = "$MIGR_VERSION" ]; then pass "$image reports $reported"; else fail "$image reports '$reported', want $MIGR_VERSION"; fi
done

sql=$(project sql sql migr_docker_sql)
cat >"$sql/migrations/20261023T120000_add_docker_sql.sql" <<'SQL'
-- migr:up
CREATE TABLE docker_sql (id bigint PRIMARY KEY);
-- migr:down
DROP TABLE docker_sql;
SQL

expect 0 "the minimal image applies a .sql project" "$MIGR_IMAGE" "$sql" up
if migr "$MIGR_IMAGE" "$sql" status 2>&1 | grep -q applied; then pass "status reports it applied"; else fail "status does not report it applied"; fi
expect 0 "and rolls it back" "$MIGR_IMAGE" "$sql" down

ts=$(project ts typescript migr_docker_ts)
echo '{"name":"docker-e2e","private":true,"type":"module"}' >"$ts/package.json"
tarball=$(cd "$SDK_DIR" && npm pack --silent --pack-destination "$ts")
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$ts:/work" --entrypoint npm "$MIGR_NODE_IMAGE" \
  install --silent --no-audit --no-fund "./$tarball"

cat >"$ts/migrations/20261023T120000_add_docker_items.ts" <<'TS'
import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s.createTable("docker_items").column("id", t.int64()).column("label", t.string(64)).primaryKey("id").apply();
  },
});
TS
cat >"$ts/migrations/20261023T120100_add_docker_items_note.ts" <<'TS'
import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s.table("docker_items").addColumn("note", t.string(256)).null().apply();
  },
});
TS
cat >"$ts/migrations/20261023T120200_widen_docker_items_label.ts" <<'TS'
import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s.table("docker_items").alterColumn("label").type(t.string(128)).wasType(t.string(64)).apply();
  },
});
TS

expect 5 "validate --require-safe refuses a retype, which rewrites the table" "$MIGR_NODE_IMAGE" "$ts" validate --require-safe
if grep -q 20261023T120200_widen_docker_items_label "$work/out"; then pass "naming the migration"; else fail "the refusal does not name the retype's migration"; sed 's/^/      /' "$work/out"; fi

rm "$ts/migrations/20261023T120200_widen_docker_items_label.ts"
expect 0 "validate --require-safe passes without it" "$MIGR_NODE_IMAGE" "$ts" validate --require-safe
expect 0 "plan --require-safe passes" "$MIGR_NODE_IMAGE" "$ts" plan --require-safe
expect 0 "the node image applies a TypeScript project" "$MIGR_NODE_IMAGE" "$ts" up
expect 0 "and rolls it back" "$MIGR_NODE_IMAGE" "$ts" down --step 2

if [ "$failures" -gt 0 ]; then echo "$failures check(s) failed"; exit 1; fi
echo "every check passed"
