#!/usr/bin/env bash
# Fails if the current commit modifies, renames or deletes a migration that exists on the base.
#
# Migrations are append-only (CLAUDE.md, "Schema migrations"): a merged file may already be
# applied in production, so a change to it would never run there. Fix forward with a new file.
# Only files present at the merge base count, so adding, renaming or editing your own
# unmerged migration is fine.
#
# usage: scripts/db-append-only.sh [BASE]   (default origin/main; CI passes the merge ref's HEAD^1)
set -euo pipefail

base_ref=${1:-origin/main}
if ! base=$(git merge-base "$base_ref" HEAD); then
  echo "db-append-only: no merge base between $base_ref and HEAD (fetch more history?)" >&2
  exit 2
fi

changed=$(git diff --name-status -M --diff-filter=MDR "$base" HEAD -- 'db/migrations/*.sql')
if [[ -z $changed ]]; then
  echo "db/migrations is append-only: no merged migration changed since $(git rev-parse --short "$base")"
  exit 0
fi

echo "db-append-only: these merged migrations were modified (M), renamed (R) or deleted (D):" >&2
while IFS=$'\t' read -r status old _; do
  echo "  ${status:0:1} $old" >&2
  if [[ -n ${GITHUB_ACTIONS:-} ]]; then
    echo "::error file=$old::Merged migrations are append-only; restore $old and fix forward with a new migration"
  fi
done <<<"$changed"
echo "db-append-only: restore them and add a new migration instead (task db:new NAME=<slug>)" >&2
exit 1
