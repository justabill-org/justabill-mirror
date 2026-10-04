#!/usr/bin/env bash
# Applies this branch's new migrations to a database that already holds data (#212).
#
# Finds the highest migration version at the merge base with BASE ("main's version"). If the
# branch adds files above it, runs db/migrations' TestUpgradeFromBase on the Spanner emulator:
# a throwaway database is migrated to that version with wrench, seeded with testdb's fixture,
# and then each new file is applied on its own. A migration that only fails on existing rows
# fails the test, which names the file. With no new migrations it says so and exits 0.
#
# usage: scripts/db-upgrade-test.sh [BASE]   (default origin/main; CI passes the merge ref's HEAD^1)
# Environment: SPANNER_EMULATOR_HOST (required), SPANNER_PROJECT, SPANNER_INSTANCE (it must
# exist), WRENCH (the wrench command).
set -euo pipefail

if [[ -z "${SPANNER_EMULATOR_HOST:-}" ]]; then
  echo "db-upgrade-test: SPANNER_EMULATOR_HOST is not set; this script only runs against the emulator" >&2
  exit 2
fi

base_ref=${1:-origin/main}
if ! base=$(git merge-base "$base_ref" HEAD); then
  echo "db-upgrade-test: no merge base between $base_ref and HEAD (fetch more history?)" >&2
  exit 2
fi

# The highest NNNNNN_ prefix among the base's migration files (10# reads it as decimal).
highest=$(git ls-tree --name-only "$base" db/migrations/ | sed -nE 's|^db/migrations/([0-9]{6})_.*\.sql$|\1|p' \
  | sort | tail -n 1)
if [[ -z $highest ]]; then
  echo "db-upgrade-test: no migrations at $(git rev-parse --short "$base"); nothing to upgrade from" >&2
  exit 2
fi
version=$((10#$highest))

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# A glob rather than find -printf (GNU-only), so it runs on macOS too; globs expand sorted.
new=""
for path in "$root"/db/migrations/[0-9][0-9][0-9][0-9][0-9][0-9]_*.sql; do
  [[ -e $path ]] || continue
  name=${path##*/}
  if ((10#${name:0:6} > version)); then
    new+="$name"$'\n'
  fi
done
new=${new%$'\n'}
if [[ -z $new ]]; then
  echo "db-upgrade-test: no migrations above version $version (the base's highest); nothing to test"
  exit 0
fi

echo "db-upgrade-test: applying to a seeded database at version $version:"
while read -r name; do echo "  $name"; done <<<"$new"
cd "$root/db"
MIGRATIONS_BASE_VERSION=$version go test -count=1 -v -run '^TestUpgradeFromBase$' ./migrations/
