#!/usr/bin/env bash
# Tests scripts/db-rebase.sh in throwaway git repos, with a stand-in for scripts/db-schema.sh that
# writes db/schema.sql as the list of migration files (the real one needs the emulator).
# Run: scripts/db-rebase-test.sh
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0
check() { # check <name> <expected> <actual>
  if [[ $2 == "$3" ]]; then echo "ok   $1"; else echo "FAIL $1: expected [$2], got [$3]"; failures=$((failures + 1)); fi
}

cat >"$tmp/schema.sh" <<'SH'
#!/usr/bin/env bash
ls db/migrations/*.sql | sed 's|db/migrations/||' >db/schema.sql
SH
chmod +x "$tmp/schema.sh"
export DB_SCHEMA=$tmp/schema.sh GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

# repo <dir>: a repo on main with migrations 000001 and 000002, and a README.
repo() {
  git init -q -b main "$1" && cd "$1" || exit 1
  mkdir -p db/migrations scripts && cp "$here/db-rebase.sh" scripts/
  echo "CREATE TABLE a;" >db/migrations/000001_a.sql
  echo "CREATE TABLE b;" >db/migrations/000002_b.sql
  echo "readme" >README.md
  "$DB_SCHEMA" && git add -A && git commit -q -m "chore: start"
}
add() { # add <file> <text>: commit a new or changed file
  mkdir -p "$(dirname "$1")" && echo "$2" >"$1" && "$DB_SCHEMA" && git add -A && git commit -q -m "feat: $1"
}
files() { ls db/migrations | tr '\n' ' ' | sed 's/ $//'; }

# A parallel PR took the branch's number: the branch's file moves up, and schema.sql follows.
(repo "$tmp/collide" >/dev/null
 git checkout -q -b feat/1-x && add db/migrations/000003_mine.sql "CREATE TABLE mine;"
 git checkout -q main && add db/migrations/000003_theirs.sql "CREATE TABLE theirs;"
 git checkout -q feat/1-x
 scripts/db-rebase.sh main >"$tmp/out" 2>&1; echo "rc=$?"
 echo "files=$(files)"
 echo "schema=$(tr '\n' ' ' <db/schema.sql | sed 's/ $//')"
 echo "log=$(git log -1 --format=%s)") >"$tmp/r1"
check "collision: exit 0" "rc=0" "$(grep rc= "$tmp/r1")"
check "collision: renumbered after main's" "files=000001_a.sql 000002_b.sql 000003_theirs.sql 000004_mine.sql" \
  "$(grep files= "$tmp/r1")"
check "collision: schema regenerated" "schema=000001_a.sql 000002_b.sql 000003_theirs.sql 000004_mine.sql" \
  "$(grep schema= "$tmp/r1")"
check "collision: commit says what moved" "log=chore(db): renumber migrations after main's 000003 (000003->000004)" \
  "$(grep log= "$tmp/r1")"

# Two of the branch's migrations move up together, in order, with no gap.
(repo "$tmp/two" >/dev/null
 git checkout -q -b feat/2-x && add db/migrations/000003_one.sql "one" && add db/migrations/000004_two.sql "two"
 git checkout -q main && add db/migrations/000003_theirs.sql "theirs" && add db/migrations/000004_more.sql "more"
 git checkout -q feat/2-x && scripts/db-rebase.sh main >/dev/null 2>&1; echo "rc=$? files=$(files)") >"$tmp/r2"
check "two files: kept in order after main's" \
  "rc=0 files=000001_a.sql 000002_b.sql 000003_theirs.sql 000004_more.sql 000005_one.sql 000006_two.sql" "$(cat "$tmp/r2")"

# Nothing collides: main's other work comes in, the branch's migration keeps its number.
(repo "$tmp/clean" >/dev/null
 git checkout -q -b feat/3-x && add db/migrations/000003_mine.sql "mine"
 git checkout -q main && add api/x.go "package x"
 git checkout -q feat/3-x && scripts/db-rebase.sh main >"$tmp/out3" 2>&1; echo "rc=$? files=$(files)"
 scripts/db-rebase.sh main 2>&1 | tail -1) >"$tmp/r3"
check "no collision: number kept" "rc=0 files=000001_a.sql 000002_b.sql 000003_mine.sql" "$(head -1 "$tmp/r3")"
check "no collision: a second run has nothing to do" \
  "db-rebase: up to date with main; migrations and db/schema.sql already match" "$(tail -1 "$tmp/r3")"

# A schema.sql that conflicts by hand-editing is replaced by the regenerated one.
(repo "$tmp/schema" >/dev/null
 git checkout -q -b feat/4-x && echo "hand edit on the branch" >db/schema.sql && git commit -qam "fix: schema"
 git checkout -q main && echo "hand edit on main" >db/schema.sql && git commit -qam "fix: schema"
 git checkout -q feat/4-x && scripts/db-rebase.sh main >/dev/null 2>&1
 echo "rc=$? schema=$(tr '\n' ' ' <db/schema.sql | sed 's/ $//') status=$(git status --porcelain | wc -l | tr -d ' ')") >"$tmp/r4"
check "schema.sql conflict: taken from main, regenerated" "rc=0 schema=000001_a.sql 000002_b.sql status=0" "$(cat "$tmp/r4")"

# Any other conflict stops it, with the merge left in progress and the file named.
(repo "$tmp/other" >/dev/null
 git checkout -q -b feat/5-x && add README.md "branch"
 git checkout -q main && add README.md "main"
 git checkout -q feat/5-x && scripts/db-rebase.sh main >/dev/null 2>"$tmp/err5"; echo "rc=$?"
 grep -c '  README.md' "$tmp/err5"; git rev-parse -q --verify MERGE_HEAD >/dev/null && echo merging) >"$tmp/r5"
check "other conflict: exit 1, file named, merge left open" "rc=1 1 merging" "$(tr '\n' ' ' <"$tmp/r5" | sed 's/ $//')"

# With origin/main as the base, main's newer commits are fetched first, even in a clone whose fetch
# refspec wouldn't update origin/main (a clone with --single-branch of the feature branch).
(repo "$tmp/upstream" >/dev/null
 git clone -q --single-branch --branch main "$tmp/upstream" "$tmp/clone" && cd "$tmp/clone" &&
   git config --unset-all remote.origin.fetch && git config remote.origin.fetch "+refs/heads/feat/*:refs/remotes/origin/feat/*"
 git checkout -q -b feat/6-x && add db/migrations/000003_mine.sql "mine"
 cd "$tmp/upstream" && add db/migrations/000003_theirs.sql "theirs"
 cd "$tmp/clone" && scripts/db-rebase.sh origin/main >/dev/null 2>&1; echo "rc=$? files=$(files)") >"$tmp/r7"
check "origin/main: fetched before the merge" \
  "rc=0 files=000001_a.sql 000002_b.sql 000003_theirs.sql 000004_mine.sql" "$(tail -1 "$tmp/r7")"

# Uncommitted changes: it doesn't start.
(repo "$tmp/dirty" >/dev/null
 echo "changed" >>README.md && scripts/db-rebase.sh main >/dev/null 2>&1; echo "rc=$?") >"$tmp/r6"
check "uncommitted changes: exit 2" "rc=2" "$(cat "$tmp/r6")"

echo
if (( failures )); then echo "$failures failed"; exit 1; fi
echo "all passed"
