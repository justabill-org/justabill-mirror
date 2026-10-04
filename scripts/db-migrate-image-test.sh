#!/usr/bin/env bash
# Builds the migrate image (db/Dockerfile) and runs it against a throwaway emulator database the
# way the production Job runs it: user 65532, a read-only root and a tmpfs /tmp. Four runs:
#   1. a fresh database gets every migration, and the log has the versions and the schema;
#   2. a second run applies nothing;
#   3. an older release's migrations (the newest file left out, as on a rollback) apply nothing;
#   4. a failing migration exits non-zero and the log points to the runbook.
# The database is dropped on exit.
#
# usage: scripts/db-migrate-image-test.sh [BASE]
# With BASE (CI passes the merge ref's HEAD^1), it skips unless the branch changes what goes into
# the image: db/Dockerfile, db/migrations/, scripts/db-migrate.sh or scripts/lib/.
# Environment: SPANNER_EMULATOR_HOST (required; the container shares the host network),
# SPANNER_PROJECT (default justabill-local), SPANNER_INSTANCE (default test-instance; it must
# exist), WRENCH (the wrench command), IMAGE (default justabill-migrate:test).
set -euo pipefail

if [[ -z "${SPANNER_EMULATOR_HOST:-}" ]]; then
  echo "db-migrate-image-test: SPANNER_EMULATOR_HOST is not set; this script only runs against the emulator" >&2
  exit 2
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
if [[ -n "${1:-}" ]]; then
  if ! base=$(git -C "$root" merge-base "$1" HEAD); then
    echo "db-migrate-image-test: no merge base between $1 and HEAD (fetch more history?)" >&2
    exit 2
  fi
  if git -C "$root" diff --quiet "$base" HEAD -- db/Dockerfile db/migrations/ scripts/db-migrate.sh scripts/lib/; then
    echo "db-migrate-image-test: the migrate image's inputs didn't change since $(git -C "$root" rev-parse --short "$base"); skipping"
    exit 0
  fi
fi

image=${IMAGE:-justabill-migrate:test}
project=${SPANNER_PROJECT:-justabill-local}
instance=${SPANNER_INSTANCE:-test-instance}
read -r -a wrench <<<"${WRENCH:-go run github.com/cloudspannerecosystem/wrench@v1.13.5}"
database="migrate_$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
target=(--project "$project" --instance "$instance" --database "$database")

work=$(mktemp -d)
created=false
cleanup() {
  if $created; then
    "${wrench[@]}" drop "${target[@]}" --directory "$work" >/dev/null 2>&1 \
      || echo "db-migrate-image-test: could not drop throwaway database $database" >&2
  fi
  rm -rf "$work"
}
trap cleanup EXIT

docker build --network host -q -f "$root/db/Dockerfile" -t "$image" "$root" >/dev/null
echo "built $image"

: >"$work/schema.sql"
"${wrench[@]}" create "${target[@]}" --directory "$work"
created=true

files=("$root"/db/migrations/[0-9][0-9][0-9][0-9][0-9][0-9]_*.sql)
newest=$((10#$(basename "${files[-1]}" | cut -c1-6)))

# run LOG [docker args...]: runs the image, saving its output to $work/LOG; returns its status.
run() {
  local log=$1
  shift
  docker run --rm --network host --read-only --tmpfs /tmp \
    -e SPANNER_EMULATOR_HOST -e SPANNER_PROJECT="$project" -e SPANNER_INSTANCE="$instance" \
    -e SPANNER_DATABASE="$database" "$@" "$image" >"$work/$log" 2>&1
}
fail() {
  echo "db-migrate-image-test: $1; the log:" >&2
  cat "$work/$2" >&2
  exit 1
}
expect() { # expect LOG TEXT
  grep -qF -- "$2" "$work/$1" || fail "run $1 didn't log \"$2\"" "$1"
}

run 1 || fail "the first run failed" 1
expect 1 "$database: version 0 before"
expect 1 "pending: ${files[0]##*/}"
expect 1 "$database: version $newest after"
expect 1 "----- BEGIN $database schema (wrench load) -----"
expect 1 "CREATE TABLE bills ("
echo "ok   a fresh database gets every migration ($newest) and the schema is logged"

run 2 || fail "the second run failed" 2
expect 2 "$database: nothing to apply"
expect 2 "$database: version $newest after"
echo "ok   a second run applies nothing"

# A migrations directory the container user (65532) can read.
mkdir -m 755 "$work/older" "$work/broken"
cp "${files[@]:0:${#files[@]}-1}" "$work/older/"
cp "${files[@]}" "$work/broken/"
printf -v bad '%06d_bad.sql' $((newest + 1))
echo 'CREATE INDEX bad_idx ON no_such_table(id);' >"$work/broken/$bad"
chmod 644 "$work"/older/* "$work"/broken/*

run 3 -v "$work/older:/app/db/migrations:ro" || fail "the rollback run failed" 3
expect 3 "nothing to apply; the database is ahead of these migrations (newest $((newest - 1)))"
echo "ok   an older release's migrations apply nothing"

if run 4 -v "$work/broken:/app/db/migrations:ro"; then
  fail "a failing migration exited 0" 4
fi
expect 4 "pending: $bad"
expect 4 "Runbook: https://github.com/justabill-org/justabill/wiki/Ops-Migrations"
echo "ok   a failing migration exits non-zero and points to the runbook"
echo "db-migrate-image-test: all passed"
