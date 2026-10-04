#!/usr/bin/env bash
# Tests scripts/db-migrations-between.sh and the DROP detection in scripts/lib/migrations.sh
# on a scratch git repository with two tagged "releases". No emulator needed.
#
# usage: scripts/db-migrations-between-test.sh
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=scripts/lib/migrations.sh
source "$root/scripts/lib/migrations.sh"
failures=0

check() { # check NAME EXPECTED ACTUAL
  if [[ $2 == "$3" ]]; then
    echo "ok   $1"
  else
    echo "FAIL $1"$'\n'"  want: ${2//$'\n'/$'\n'        }"$'\n'"  got:  ${3//$'\n'/$'\n'        }"
    failures=$((failures + 1))
  fi
}

drop() { # drop SQL → yes/no
  if migrations_has_drop <<<"$1"; then echo yes; else echo no; fi
}

check "DROP TABLE" yes "$(drop 'DROP TABLE bills;')"
check "lower-case drop column" yes "$(drop 'alter table bills drop column title;')"
check "DROP after a quoted --" yes "$(drop "UPDATE t SET a = '--' WHERE TRUE; DROP INDEX x;")"
check "multi-line statement" yes "$(drop $'ALTER TABLE bills\n  DROP CONSTRAINT fk_x;')"
check "CREATE TABLE" no "$(drop 'CREATE TABLE t (id STRING(36)) PRIMARY KEY (id);')"
check "ON DELETE CASCADE" no "$(drop 'CREATE TABLE c (id INT64) PRIMARY KEY (id), INTERLEAVE IN PARENT p ON DELETE CASCADE;')"
check "-- comment" no "$(drop $'-- DROP the old index in the next release\nCREATE INDEX i ON t(a);')"
check "# comment" no "$(drop '# drop later')"
check "block comment" no "$(drop $'/* We will\n DROP this */ CREATE INDEX i ON t(a);')"
check "quoted text" no "$(drop "INSERT INTO notes (text) VALUES ('drop by any time'), (\"Drop\");")"
# shellcheck disable=SC2016 # the backquotes are SQL, not a command substitution
check "backquoted identifier" no "$(drop 'CREATE TABLE `drop` (id INT64) PRIMARY KEY (id);')"
check "word containing drop" no "$(drop 'CREATE INDEX dropoff_idx ON t(dropped_at);')"
check "version" 12 "$(migrations_version db/migrations/000012_add_x.sql)"

# A scratch repository with the scripts and two releases.
repo=$(mktemp -d)
trap 'rm -rf "$repo"' EXIT
mkdir -p "$repo/scripts/lib" "$repo/db/migrations"
cp "$root/scripts/db-migrations-between.sh" "$repo/scripts/"
cp "$root/scripts/lib/migrations.sh" "$repo/scripts/lib/"
g() { git -C "$repo" -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false "$@"; }
g init -q
echo 'CREATE TABLE a (id INT64) PRIMARY KEY (id);' >"$repo/db/migrations/000001_baseline.sql"
g add -A && g commit -qm one && g tag v0.1.0
echo 'CREATE INDEX a_by_id ON a(id);' >"$repo/db/migrations/000002_add_index.sql"
echo '-- a comment' >"$repo/db/migrations/README.md"
echo 'DROP INDEX a_by_id;' >"$repo/db/migrations/000003_drop_index.sql"
g add -A && g commit -qm two && g tag v0.2.0
echo 'CREATE INDEX a_by_id2 ON a(id);' >"$repo/db/migrations/000004_add_index.sql"
g add -A && g commit -qm three && g tag v0.3.0
echo '-- edited after release' >>"$repo/db/migrations/000001_baseline.sql"
g rm -q db/migrations/000002_add_index.sql && g add -A && g commit -qm four && g tag v0.4.0

between() { "$repo/scripts/db-migrations-between.sh" "$@"; }
check "with a DROP" "**Migrations from v0.1.0 to v0.2.0:** 2 to apply, **1 with DROP**: check each one follows expand/contract (its code stopped using what it drops in an earlier release) before merging.

- \`db/migrations/000002_add_index.sql\`
- \`db/migrations/000003_drop_index.sql\` **DROP**" "$(between v0.1.0 v0.2.0)"
check "without a DROP" "**Migrations from v0.2.0 to v0.3.0:** 1 to apply, no DROP.

- \`db/migrations/000004_add_index.sql\`" "$(between v0.2.0 v0.3.0)"
check "none" "**Migrations from v0.3.0 to v0.3.0:** none." "$(between v0.3.0 v0.3.0)"
check "changed and deleted" "**Migrations from v0.3.0 to v0.4.0:** none.

**Warning:** merged migrations changed (M) or deleted (D) between v0.3.0 and v0.4.0. Production won't re-run them; fix forward with a new migration:

- M \`db/migrations/000001_baseline.sql\`
- D \`db/migrations/000002_add_index.sql\`" "$(between v0.3.0 v0.4.0)"
status=0
between v0.1.0 nope >/dev/null 2>&1 || status=$?
check "unknown ref exits 2" 2 "$status"

if ((failures > 0)); then
  echo "db-migrations-between-test: $failures failed" >&2
  exit 1
fi
echo "db-migrations-between-test: all passed"
