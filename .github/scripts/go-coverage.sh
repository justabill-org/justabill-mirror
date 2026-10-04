#!/usr/bin/env bash
# shellcheck disable=SC2016 # backticks in printf formats are Markdown
# Checks a Go module's coverage against the floors in its .testcoverage.yml
# (docs/design/172-testing-linting.md), writes coverage.html, and prints a per-package table. In CI
# it also appends the table and the check's result to the job summary.
#
#   cd <db|api|pipeline> && go test -coverprofile=coverage.out ./... && ../.github/scripts/go-coverage.sh
#
# The committed floors are the hard minimum, and PRs don't edit them. A PR also fails when the
# module's total drops below main's, rounded down (coverage-vs-main.sh, which needs GH_TOKEN in CI).
set -euo pipefail

profile=coverage.out
go_test_coverage=github.com/vladopajic/go-test-coverage/v2@v2.19.0

module=$(go list -m)
name=${PWD##*/}

go tool cover -html="$profile" -o coverage.html

# Each package's own coverage (no -coverpkg), cmd/ and test helpers included, from the profile's
# blocks: "<import path>/<file>.go:<pos> <statements> <count>".
table=$(awk -v prefix="$module/" '
  NR > 1 {
    file = substr($1, 1, index($1, ":") - 1)
    sub("^" prefix, "", file)
    pkg = file
    if (!sub(/\/[^\/]*$/, "", pkg)) pkg = "."
    total[pkg] += $2
    if ($3 > 0) covered[pkg] += $2
  }
  END {
    for (pkg in total)
      printf "| `%s` | %.1f%% | %d |\n", pkg, 100 * covered[pkg] / total[pkg], total[pkg]
  }' "$profile" | sort)

status=0
report=$(go run "$go_test_coverage" --config=.testcoverage.yml 2>&1) || status=$?
# Everything up to the total; the per-file list of uncovered lines stays in the log.
result=$(sed -n '1,/^Total test coverage/p' <<<"$report")

printf '%s\n\n| Package | Coverage | Statements |\n|---|---:|---:|\n%s\n\n' "$report" "$table"

if [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
  {
    printf '### Coverage: `%s/`\n\n' "$name"
    if ((status)); then
      printf '**Below the committed floor** in `%s/.testcoverage.yml`. Add tests; lower a floor only with the maintainer'"'"'s OK.\n\n' "$name"
    fi
    printf '```\n%s\n```\n\n' "$result"
    printf '<details><summary>Every package (cmd/ and test helpers included)</summary>\n\n'
    printf '| Package | Coverage | Statements |\n|---|---:|---:|\n%s\n\n</details>\n\n' "$table"
  } >>"$GITHUB_STEP_SUMMARY"
fi

# The module's total in istanbul's summary shape, compared with main's on a PR.
total=$(sed -nE 's/^Total test coverage: ([0-9.]+)%.*/\1/p' <<<"$report")
if [[ -n $total ]]; then
  printf '{"total":{"statements":{"pct":%s}}}\n' "$total" >coverage-summary.json
  "$(dirname "$0")/coverage-vs-main.sh" "$name" coverage-summary.json || status=1
fi

exit "$status"
