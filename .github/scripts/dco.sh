#!/usr/bin/env bash
# Checks that every commit a pull request adds is signed off (the Developer Certificate of Origin,
# CONTRIBUTING.md): its message has a "Signed-off-by: Name <email>" line with the commit author's
# email, as `git commit -s` writes it. Merge commits are skipped. CI's DCO job runs it.
#
# usage: .github/scripts/dco.sh <base> <head>     (the commits in <base>..<head>)
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <base> <head>" >&2
  exit 2
fi
base=$1 head=$2

unsigned=0
checked=0
while IFS= read -r commit; do
  checked=$((checked + 1))
  email=$(git log -1 --format=%ae "$commit")
  subject=$(git log -1 --format=%s "$commit")
  # The trailers, unfolded; an email compares case-insensitively.
  if ! git log -1 --format='%(trailers:key=Signed-off-by,valueonly,unfold)' "$commit" |
    grep -qiF "<$email>"; then
    unsigned=$((unsigned + 1))
    msg="$(git rev-parse --short "$commit") \"$subject\" has no Signed-off-by line for its author <$email>"
    if [[ -n ${GITHUB_ACTIONS:-} ]]; then echo "::error title=Unsigned commit::$msg"; else echo "$msg" >&2; fi
  fi
done < <(git rev-list --no-merges --reverse "$base..$head")

if ((unsigned)); then
  cat >&2 <<'HELP'

Sign off each commit with `git commit -s`. To sign off the commits already on your branch, run
`git rebase --signoff <base>` (the main you branched from), then `git push --force-with-lease`.
See "Sign off your commits (DCO)" in CONTRIBUTING.md.
HELP
  exit 1
fi
echo "dco: all $checked commits are signed off"
