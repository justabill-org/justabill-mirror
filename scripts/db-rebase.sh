#!/usr/bin/env bash
# Merges BASE (default origin/main) into the branch and settles the migration work that comes with
# it, so none of it is done by hand:
#
# - db/schema.sql is generated, so a conflict there takes BASE's copy (it's regenerated below);
# - this branch's new migrations are renumbered to follow BASE's latest when a parallel PR took
#   their numbers. Versions stay 1..N with no gaps (db/migrations/migrations_test.go), and the
#   files are only this branch's own, which db-append-only.sh allows;
# - db/schema.sql is regenerated from the result (scripts/db-schema.sh).
#
# Design 87 has a PR renumber its migration as a fix-forward when another takes its number; two
# PRs doing that by hand (#325, #320) are the lesson in db/CLAUDE.md. Any other conflict stops the
# script with the merge in progress, for you to finish and then run it again (or `git merge
# --abort`). Exit codes: 0 done (with or without commits), 1 other conflicts, 2 can't start.
#
# usage: scripts/db-rebase.sh [BASE]   (task db:rebase; DB_SCHEMA overrides the regenerate command)
set -euo pipefail

base=${1:-origin/main}
schema_cmd=${DB_SCHEMA:-scripts/db-schema.sh}
root=$(git rev-parse --show-toplevel)
cd "$root"

if [[ -n $(git status --porcelain --untracked-files=no) ]]; then
  echo "db-rebase: commit or stash your changes first" >&2
  exit 2
fi
if [[ $base == origin/* ]]; then
  # An explicit refspec updates origin/<branch> even in a clone without the default fetch refspec.
  branch=${base#origin/}
  git fetch -q origin "+refs/heads/$branch:refs/remotes/origin/$branch" ||
    { echo "db-rebase: can't fetch $base" >&2; exit 2; }
fi

version() { sed -nE 's|^db/migrations/([0-9]{6})_[^/]+\.sql$|\1|p'; }

# 1. Merge. Only db/schema.sql may conflict: BASE's copy stands in until it's regenerated.
if ! git merge --no-edit -q "$base" >/dev/null 2>&1; then
  conflicted=$(git diff --name-only --diff-filter=U)
  if [[ -z $conflicted ]]; then
    echo "db-rebase: git merge $base failed" >&2
    exit 2
  fi
  other=$(grep -vx 'db/schema.sql' <<<"$conflicted" || true)
  if [[ -n $other ]]; then
    echo "db-rebase: these conflict outside db/schema.sql; resolve them, commit, then run this again:" >&2
    sed 's/^/  /' <<<"$other" >&2
    exit 1
  fi
  git checkout -q --theirs db/schema.sql
  git add db/schema.sql
  git commit -q --no-edit
fi

# 2. Renumber this branch's new migrations to follow BASE's latest, keeping their order.
latest=$(git ls-tree -r --name-only "$base" db/migrations/ | version | sort | tail -1)
next=$((10#${latest:-0} + 1))
renamed=()
while IFS= read -r file; do
  [[ -n $file ]] || continue
  v=$(version <<<"$file")
  want=$(printf '%06d' "$next")
  if [[ $v != "$want" ]]; then
    git mv "$file" "db/migrations/${want}_${file#db/migrations/[0-9][0-9][0-9][0-9][0-9][0-9]_}"
    renamed+=("$v->$want")
  fi
  next=$((next + 1))
done < <(git diff --name-only --no-renames --diff-filter=A "$base" HEAD -- 'db/migrations/*.sql' | sort)

# 3. Regenerate db/schema.sql from the migrations as they are now.
"$schema_cmd"
git add db/schema.sql
if [[ ${#renamed[@]} -gt 0 ]] || ! git diff --cached --quiet; then
  msg="chore(db): regenerate db/schema.sql after merging ${base#origin/}"
  [[ ${#renamed[@]} -gt 0 ]] && msg="chore(db): renumber migrations after ${base#origin/}'s ${latest:-000000} (${renamed[*]})"
  git commit -q -m "$msg"
  echo "db-rebase: $msg"
else
  echo "db-rebase: up to date with $base; migrations and db/schema.sql already match"
fi
