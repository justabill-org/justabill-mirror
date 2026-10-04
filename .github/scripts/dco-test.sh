#!/usr/bin/env bash
# Tests dco.sh on a scratch repository: signed commits pass; an unsigned commit, or one signed off by
# someone other than its author, fails and is named; a merge commit needs no sign-off.
#
# usage: .github/scripts/dco-test.sh
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
failures=0
while IFS= read -r var; do unset "$var"; done < <(compgen -e | grep '^GIT_' || true)
unset GITHUB_ACTIONS

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"
git init -q -b main
git config core.hooksPath /dev/null
git config commit.gpgsign false
c() { # c NAME EMAIL MESSAGE: an empty commit by NAME <EMAIL>
  git -c user.name="$1" -c user.email="$2" commit -q --allow-empty -m "$3"
}
run() { # run BASE HEAD: sets status and out
  status=0
  out=$("$here/dco.sh" "$1" "$2" 2>&1) || status=$?
}
check() { # check NAME EXPECTED ACTUAL
  if [[ $2 == "$3" ]]; then echo "ok   $1"; else echo "FAIL $1: want $2, got $3"$'\n'"$out"; failures=$((failures + 1)); fi
}
has() { # has NAME TEXT
  if [[ $out == *"$2"* ]]; then echo "ok   $1"; else echo "FAIL $1: no '$2' in"$'\n'"$out"; failures=$((failures + 1)); fi
}

c Base base@example.com "chore: base"
git branch -q base
c Ada ada@example.com $'feat: one\n\nSigned-off-by: Ada <ada@example.com>'
c Ada Ada@Example.com $'fix: two\n\nSigned-off-by: Ada Lovelace <ada@example.com>'
run base HEAD
check "signed commits pass, the email in any case" 0 "$status"

git checkout -q -b other base
c Bob bob@example.com $'docs: other\n\nSigned-off-by: Bob <bob@example.com>'
git checkout -q main
git -c user.name=Ada -c user.email=ada@example.com merge -q --no-ff --no-edit other
run base HEAD
check "a merge commit needs no sign-off" 0 "$status"

c Ada ada@example.com "test: unsigned"
c Eve eve@example.com $'fix: someone else\n\nSigned-off-by: Ada <ada@example.com>'
c Ada ada@example.com $'fix: in the body\n\nSee Signed-off-by: Ada <ada@example.com> above.'
run base HEAD
check "unsigned commits fail" 1 "$status"
has "an unsigned commit is named" '"test: unsigned" has no Signed-off-by line for its author <ada@example.com>'
has "a commit signed off by someone else is named" '"fix: someone else" has no Signed-off-by line for its author <eve@example.com>'
has "a sign-off in the text, not a trailer, is named" '"fix: in the body" has no Signed-off-by'
check "only those three" 3 "$(grep -c 'has no Signed-off-by' <<<"$out")"

if ((failures)); then
  echo "dco-test: $failures failed"
  exit 1
fi
echo "dco-test: all passed"
