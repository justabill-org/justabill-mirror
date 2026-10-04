#!/usr/bin/env bash
# Applies pending db/migrations to one Spanner database with wrench (`migrate up`).
#
# It logs the version before, the pending files (flagging any with DROP) and the version after.
# A database already at or above the newest file (a rollback to an older release) gets nothing
# applied. If a migration fails, wrench leaves the version dirty; the script exits non-zero and
# points to the runbook. With --audit it then prints the schema Spanner reports (wrench load)
# as the run's audit record. The migrate image (db/Dockerfile) runs it that way as a Kubernetes
# Job before each production rollout (design 28, "How production migrations run").
#
# It refuses a database that has tables but no recorded version: one built from db/schema.sql
# before migrations existed. wrench would re-apply the baseline, fail on the first table and
# mark version 1 dirty. Run `task db:adopt` (wrench migrate set 1) on such a database first.
#
# usage: scripts/db-migrate.sh [--audit]
# Environment: SPANNER_PROJECT, SPANNER_INSTANCE, SPANNER_DATABASE (required), WRENCH (the
# wrench command). With SPANNER_EMULATOR_HOST set, it talks to the emulator. It writes a
# temporary directory under TMPDIR (default /tmp), which must be writable.
set -euo pipefail

runbook="https://github.com/justabill-org/justabill/wiki/Ops-Migrations"
audit=false
case "${1:-}" in
  "") ;;
  --audit) audit=true ;;
  *) echo "usage: $0 [--audit]" >&2; exit 2 ;;
esac

: "${SPANNER_PROJECT:?}" "${SPANNER_INSTANCE:?}" "${SPANNER_DATABASE:?}"

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=scripts/lib/migrations.sh
source "$root/scripts/lib/migrations.sh"
read -r -a wrench <<<"${WRENCH:-go run github.com/cloudspannerecosystem/wrench@v1.13.5}"
target=(
  --project "$SPANNER_PROJECT"
  --instance "$SPANNER_INSTANCE"
  --database "$SPANNER_DATABASE"
)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

version=$("${wrench[@]}" migrate version "${target[@]}" --directory "$root/db")
if [[ $version == "No migrations." ]]; then
  "${wrench[@]}" load "${target[@]}" --directory "$work"
  if grep -E '^CREATE TABLE ' "$work/schema.sql" | grep -vq '^CREATE TABLE SchemaMigrations '; then
    echo "db-migrate: $SPANNER_DATABASE has tables but no migration version." \
      "If it was built from db/schema.sql before migrations existed, run \`task db:adopt\`" \
      "(or recreate it), then retry." >&2
    exit 1
  fi
  version=0
fi
echo "$SPANNER_DATABASE: version $version before"

# A glob rather than find -printf (GNU-only), so it runs on macOS and busybox too; globs expand sorted.
pending=0
newest=0
for path in "$root"/db/migrations/[0-9][0-9][0-9][0-9][0-9][0-9]_*.sql; do
  [[ -e $path ]] || continue
  v=$(migrations_version "$path")
  newest=$v
  ((v > version)) || continue
  pending=$((pending + 1))
  if migrations_has_drop <"$path"; then
    echo "  pending: ${path##*/} (DROP)"
  else
    echo "  pending: ${path##*/}"
  fi
done
if ((pending == 0)); then
  if ((version > newest)); then
    echo "$SPANNER_DATABASE: nothing to apply; the database is ahead of these migrations (newest $newest)"
  else
    echo "$SPANNER_DATABASE: nothing to apply"
  fi
fi

if ! "${wrench[@]}" migrate up "${target[@]}" --directory "$root/db"; then
  echo "db-migrate: a migration failed on $SPANNER_DATABASE, which is probably left at a dirty" \
    "version; nothing after it was applied. Runbook: $runbook" >&2
  exit 1
fi
echo "$SPANNER_DATABASE: version $("${wrench[@]}" migrate version "${target[@]}" --directory "$root/db") after"

if $audit; then
  "${wrench[@]}" load "${target[@]}" --directory "$work" --schema_file audit.sql
  echo "----- BEGIN $SPANNER_DATABASE schema (wrench load) -----"
  cat "$work/audit.sql"
  echo "----- END $SPANNER_DATABASE schema -----"
fi
