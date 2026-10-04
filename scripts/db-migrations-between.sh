#!/usr/bin/env bash
# Lists the migrations a deploy from release FROM to release TO applies, as Markdown for the
# deploy's bump PR (design 28, "Deploys"), and flags each file that contains DROP outside
# comments and quoted text. The maintainer reviews those before merging the bump: that is #87's
# DROP gate. It also warns about merged migrations changed or removed between the two, which
# production would never re-run (CI's append-only check should have caught them).
#
# usage: scripts/db-migrations-between.sh FROM TO   (git refs, e.g. v0.2.0 v0.3.0)
# Prints the Markdown on stdout and exits 0, whatever it finds; 2 on a usage error.
set -euo pipefail

if (($# != 2)); then
  echo "usage: $0 FROM TO   (git refs, e.g. v0.2.0 v0.3.0)" >&2
  exit 2
fi
from=$1
to=$2
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=scripts/lib/migrations.sh
source "$root/scripts/lib/migrations.sh"

for ref in "$from" "$to"; do
  if ! git -C "$root" rev-parse --verify --quiet "$ref^{commit}" >/dev/null; then
    echo "db-migrations-between: unknown ref $ref (fetch tags?)" >&2
    exit 2
  fi
done

migration_files() {
  git -C "$root" ls-tree --name-only "$1" db/migrations/ | grep -E '^db/migrations/[0-9]{6}_[^/]*\.sql$' || true
}

added=$(comm -13 <(migration_files "$from") <(migration_files "$to"))
changed=$(git -C "$root" diff --name-status --no-renames --diff-filter=MD "$from" "$to" -- 'db/migrations/*.sql')

lines=()
drops=0
while read -r path; do
  [[ -n $path ]] || continue
  if git -C "$root" show "$to:$path" | migrations_has_drop; then
    lines+=("- \`$path\` **DROP**")
    drops=$((drops + 1))
  else
    lines+=("- \`$path\`")
  fi
done <<<"$added"

if ((${#lines[@]} == 0)); then
  echo "**Migrations from $from to $to:** none."
else
  summary="${#lines[@]} to apply"
  if ((drops > 0)); then
    summary+=", **$drops with DROP**: check each one follows expand/contract (its code stopped using what it drops in an earlier release) before merging"
  else
    summary+=", no DROP"
  fi
  echo "**Migrations from $from to $to:** $summary."
  echo
  printf '%s\n' "${lines[@]}"
fi

if [[ -n $changed ]]; then
  echo
  echo "**Warning:** merged migrations changed (M) or deleted (D) between $from and $to." \
    "Production won't re-run them; fix forward with a new migration:"
  echo
  while IFS=$'\t' read -r status path; do
    echo "- $status \`$path\`"
  done <<<"$changed"
fi
