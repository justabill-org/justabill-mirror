#!/usr/bin/env bash
# shellcheck disable=SC2016 # backticks in printf formats are Markdown
# Compares a PR's coverage with main's (docs/design/172-testing-linting.md, amended 2026-09-27).
#
#   coverage-vs-main.sh <name> <summary.json>
#
# PRs no longer raise the committed floors: every PR edited the same threshold lines, so any two
# open PRs conflicted. Instead each CI run keeps its coverage summary as an artifact
# (coverage-summary-<name>), and a PR fails when a total drops below main's, rounded down to a
# whole percent. Coverage still only goes up. The committed floors stay as the hard minimum and
# are raised by hand now and then, never in a feature PR.
#
# <summary.json> has istanbul's json-summary shape, {"total": {"<metric>": {"pct": <number>}}}.
# It's copied to coverage-summary/<name>.json for the upload step. On a PR the script finds the
# newest main run that has this module's summary and ran on the PR's base or before it (HEAD^1 of
# the merge ref, so it needs fetch-depth 2), and compares each total. With nothing to compare
# (the module hasn't run on main lately, or the artifacts expired) it passes with a note, and the
# committed floors still apply. Needs GH_TOKEN with actions: read.
set -euo pipefail

name=$1
summary=$2
mkdir -p coverage-summary
cp "$summary" "coverage-summary/$name.json"
[[ ${GITHUB_EVENT_NAME:-} == pull_request ]] || exit 0

note() {
  printf '%s\n' "$1"
  [[ -z ${GITHUB_STEP_SUMMARY:-} ]] || printf '%s\n\n' "$1" >>"$GITHUB_STEP_SUMMARY"
}

if ! base=$(git rev-parse --verify --quiet HEAD^1); then
  note "Coverage vs main (\`$name/\`): no base commit to compare with (the checkout needs fetch-depth 2)."
  exit 0
fi

repo=$GITHUB_REPOSITORY
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
found=""
runs=$(gh run list -R "$repo" --workflow ci.yml --branch main --event push --status completed --limit 30 \
  --json databaseId,headSha --jq '.[] | "\(.databaseId) \(.headSha)"' 2>/dev/null) || runs=""
while read -r id sha; do
  [[ -n $id ]] || continue
  # "identical" or "ahead": the PR's base is that run's commit or comes after it.
  status=$(gh api "repos/$repo/compare/$sha...$base" --jq .status 2>/dev/null) || continue
  [[ $status == identical || $status == ahead ]] || continue
  if gh run download "$id" -R "$repo" -n "coverage-summary-$name" -D "$tmp" >/dev/null 2>&1; then
    found=$sha
    break
  fi
done <<<"$runs"

if [[ -z $found || ! -f $tmp/$name.json ]]; then
  note "Coverage vs main (\`$name/\`): no recent main coverage to compare with; the committed floors apply."
  exit 0
fi

fail=0
rows=""
while IFS=$'\t' read -r metric pr main; do
  [[ $pr =~ ^[0-9.]+$ && $main =~ ^[0-9.]+$ ]] || continue
  floor=${main%.*}
  result=ok
  if awk -v a="$pr" -v b="$floor" 'BEGIN { exit !(a < b) }'; then
    result="**below main**"
    fail=1
  fi
  rows+="| $metric | $pr% | $main% | $floor% | $result |"$'\n'
done < <(jq -r --slurpfile m "$tmp/$name.json" '
  .total | to_entries[] | [.key, (.value.pct | tostring), (($m[0].total[.key].pct // "") | tostring)] | @tsv' "$summary")

report=$(printf '### Coverage vs main: `%s/`\n\nMain at `%s`.\n\n| Metric | This PR | Main | Floor | |\n|---|---:|---:|---:|---|\n%s' \
  "$name" "${found:0:7}" "$rows")
if ((fail)); then
  report+=$'\n'"**Coverage fell below main's.** Add tests for the code this PR adds or changes. The floor is main's"
  report+=" coverage rounded down, so it moves up by itself as tested code merges; don't edit the committed floors."
fi
printf '%s\n' "$report"
[[ -z ${GITHUB_STEP_SUMMARY:-} ]] || printf '%s\n\n' "$report" >>"$GITHUB_STEP_SUMMARY"
exit "$fail"
