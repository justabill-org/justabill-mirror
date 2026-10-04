#!/usr/bin/env bash
# Tests scripts/e2e-flakes.sh: its log parsing against saved excerpts of real Web E2E job logs, and
# the whole report against a stand-in for gh that serves them, times out and loses logs. In
# testdata/e2e-flakes, <case>.json is a job and <case>-log.txt an excerpt of its log: a flaky job, a
# failed one, one that failed at setup, and one cancelled (no log).
# Run: scripts/e2e-flakes-test.sh
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
data=$here/testdata/e2e-flakes
script=$here/e2e-flakes.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0
check() { # check <name> <expected> <actual>
  if [[ $2 == "$3" ]]; then echo "ok   $1"; else
    echo "FAIL $1:"; diff <(echo "$2") <(echo "$3") | sed 's/^/     /'; failures=$((failures + 1))
  fi
}
contains() { # contains <name> <line> <text>: the text has the line, spaces squeezed
  if tr -s ' ' <<<"$3" | grep -qxF -- "$(tr -s ' ' <<<"$2")"; then echo "ok   $1"; else
    echo "FAIL $1: no line [$2] in:"; printf '%s\n' "$3" | sed 's/^/     /'; failures=$((failures + 1))
  fi
}
T=$'\t'

# --- classify: one job and its log ---

# A test failed, then passed on its retry. The annotation repeating the error (lines without a
# timestamp) isn't counted again, and the title loses its line number and the summary's "─" padding.
check "flaky: the job is flake-only, with the test and its failing step" \
  "J${T}accounts on${T}111082334399${T}flaky${T}
T${T}accounts on${T}111082334399${T}flaky${T}sign-in.spec.ts › a signed-in visitor on /login goes straight to ?next=${T}flows.ts:102: gave up: sign-in attempt 3 stuck (frame.waitForFunction: Timeout 10000ms exceeded.; frames: http:..." \
  "$("$script" classify "$data/flaky.json" "$data/flaky-log.txt")"

# Two tests failed on both attempts: a real failure; the step comes from the first attempt.
check "failed: a real failure, each test with its step" \
  "J${T}accounts off${T}111135974797${T}real${T}
T${T}accounts off${T}111135974797${T}failed${T}a11y.spec.ts › light scheme › a bill's Text tab with its companion bills open is accessible (light)${T}a11y.spec.ts:82: locator.click: Test timeout of 30000ms exceeded.
T${T}accounts off${T}111135974797${T}failed${T}a11y.spec.ts › dark scheme › a bill's Text tab with its companion bills open is accessible (dark)${T}a11y.spec.ts:82: locator.click: Test timeout of 30000ms exceeded." \
  "$("$script" classify "$data/failed.json" "$data/failed-log.txt")"

# The API didn't build, so the smoke step never ran: setup, named by the step that failed.
check "setup failure: counted apart, with the step that failed" \
  "J${T}accounts off${T}111144249658${T}setup${T}Build the API" \
  "$("$script" classify "$data/setup.json" "$data/setup-log.txt")"

# Each attempt failed differently: the step is the first attempt's.
cat >"$tmp/retry.log" <<'LOG'
2026-10-03T00:00:01.0000000Z   1) [chromium] › e2e/browse.spec.ts:4:1 › browse a bill ───────────────────
2026-10-03T00:00:01.0000000Z     Error: first attempt
2026-10-03T00:00:01.0000000Z         at /work/web/e2e/browse.spec.ts:13:3
2026-10-03T00:00:01.0000000Z     Retry #1 ─────────────────────────────────────────────────────────────
2026-10-03T00:00:01.0000000Z     Error: second attempt
2026-10-03T00:00:01.0000000Z         at /work/web/e2e/browse.spec.ts:20:3
2026-10-03T00:00:01.0000000Z   1 failed
2026-10-03T00:00:01.0000000Z     [chromium] › e2e/browse.spec.ts:4:1 › browse a bill ───────────────────
LOG
check "a test that failed twice: the first attempt's step" \
  "J${T}accounts off${T}111135974797${T}real${T}
T${T}accounts off${T}111135974797${T}failed${T}browse.spec.ts › browse a bill${T}browse.spec.ts:13: first attempt" \
  "$("$script" classify "$data/failed.json" "$tmp/retry.log")"

# A runner that couldn't check out: the action is named without its pinned commit.
jq -c '.steps |= map(if .name == "Build the API" then .conclusion = "success" elif
  (.name | startswith("Run actions/checkout@")) then .conclusion = "failure" else . end)' "$data/setup.json" >"$tmp/co.json"
check "setup failure in an action: named without its commit" \
  "J${T}accounts off${T}111144249658${T}setup${T}actions/checkout" "$("$script" classify "$tmp/co.json")"

check "cancelled: the runner shut down during the smoke step" \
  "J${T}accounts off${T}111255337356${T}cancelled${T}" "$("$script" classify "$data/cancelled.json")"

check "a failed smoke step without its log is unreadable, with why" \
  "J${T}accounts on${T}111082334399${T}unreadable${T}log expired" \
  "$(E2E_FLAKES_LOG_ERROR="log expired" "$script" classify "$data/flaky.json" "$tmp/missing.log")"

# The smoke step failed, but the log has no Playwright summary (e.g. the web server never started).
check "a smoke failure with no summary is other, not a pass or a flake" \
  "J${T}accounts on${T}111082334399${T}other${T}no Playwright summary" \
  "$("$script" classify "$data/flaky.json" "$data/setup-log.txt")"

# --- the whole report, against a stand-in for gh ---
#
# Runs on 2026-10-01 (no runs on 10-02):
#   101  PR 10 (api/ only)  attempt 1: accounts on flaky, accounts off passed; attempt 2 (a re-run):
#        accounts on passed. Its first job list request times out.
#   102  PR 11 (web/)       accounts off failed (real), accounts off setup failure, cancelled
#   103  PR 10              its job list never comes back
#   104  no PR on the run (merged since; PR 12, api/ only, by commit)  accounts on, log expired
mkdir -p "$tmp/bin" "$tmp/state"
job() { # job <fixture> <id> <attempt> [<smoke conclusion>]: a fixture job, renumbered
  jq -c --argjson id "$2" --argjson a "$3" --arg s "${4:-}" '.id = $id | .run_attempt = $a |
    if $s == "" then . else .conclusion = $s | .steps |= map(if (.name | startswith("Smoke tests")) and
    .conclusion != "skipped" then .conclusion = $s else . end) end' "$data/$1.json"
}
other_job='{"id":9,"name":"API Tests","run_attempt":1,"conclusion":"success","steps":[{"name":"Test","conclusion":"success"}]}'
aggregate='{"id":8,"name":"Web E2E","run_attempt":1,"conclusion":"failure","steps":[{"name":"Both suites passed","conclusion":"failure"}]}'
echo "{\"jobs\":[$(job flaky 1001 1),$(job failed 1002 1 success),$other_job,$aggregate]}" >"$tmp/jobs-101-p1"
echo "{\"jobs\":[$(job flaky 1003 2 success)]}" >"$tmp/jobs-101-p2"
echo "{\"jobs\":[$(job failed 1021 1),$(job setup 1022 1),$(job cancelled 1023 1)]}" >"$tmp/jobs-102"
echo "{\"jobs\":[$(job flaky 1041 1)]}" >"$tmp/jobs-104"
cat >"$tmp/runs-2026-10-01" <<'JSON'
{"workflow_runs":[{"id":101,"head_sha":"a1","pull_requests":[{"number":10}]},{"id":102,"head_sha":"b2","pull_requests":[{"number":11}]},
{"id":103,"head_sha":"c3","pull_requests":[{"number":10}]}]}
{"workflow_runs":[{"id":104,"head_sha":"d4","pull_requests":[]}]}
JSON
cat >"$tmp/bin/gh" <<SH
#!/usr/bin/env bash
# gh's stand-in: gh api [--paginate] [--jq <expr>] [--allow-escape-sequences] <path>
[[ \$1 == auth ]] && exit 0
shift; expr=""; path=""
while ((\$#)); do case \$1 in --jq) expr=\$2; shift 2 ;; --*) shift ;; *) path=\$1; shift ;; esac; done
echo "\$path" >>"$tmp/state/calls"
out() { if [[ -n \$expr ]]; then jq -r "\$expr"; else cat; fi; }
case \$path in
  *workflows/ci.yml/runs*created=2026-10-01*) out <"$tmp/runs-2026-10-01" ;;
  *workflows/ci.yml/runs*) echo '{"workflow_runs":[]}' | out ;;
  */runs/101/jobs*)
    if [[ ! -e "$tmp/state/101" ]]; then touch "$tmp/state/101"; echo "context deadline exceeded" >&2; exit 1; fi
    # Every attempt's jobs only with filter=all; the latest attempt's otherwise.
    if [[ \$path == *filter=all* ]]; then cat "$tmp/jobs-101-p1" "$tmp/jobs-101-p2"; else cat "$tmp/jobs-101-p2"; fi ;;
  */runs/102/jobs*) cat "$tmp/jobs-102" ;;
  */runs/103/jobs*) echo "HTTP 502: Bad Gateway" >&2; exit 1 ;;
  */runs/104/jobs*) cat "$tmp/jobs-104" ;;
  */jobs/1001/logs) cat "$data/flaky-log.txt" ;;
  */jobs/1021/logs) cat "$data/failed-log.txt" ;;
  */jobs/1041/logs) echo "HTTP 410: Gone" >&2; exit 1 ;;
  */commits/d4/pulls) echo '[{"number":12}]' | out ;;
  */pulls/10/files*) echo '[{"filename":"api/x.go"}]' | out ;;
  */pulls/11/files*) echo '[{"filename":"api/x.go"},{"filename":"web/src/x.ts"}]' | out ;;
  */pulls/12/files*) echo '[{"filename":"db/x.go"}]' | out ;;
  *) echo "HTTP 404: unexpected \$path" >&2; exit 1 ;;
esac
SH
chmod +x "$tmp/bin/gh"
run() { PATH="$tmp/bin:$PATH" E2E_FLAKES_RETRIES=2 E2E_FLAKES_PARALLEL=2 "$script" "$@" 2>&1; }

out=$(run --since 2026-10-01 --until 2026-10-02)
contains "report: the header counts every run and what couldn't be read" \
  "Re-run attempts count as their own jobs. Couldn't read: 1 runs' job lists, 1 job logs." "$out"
contains "report: 4 runs" "Web E2E on pull requests, 2026-10-01 to 2026-10-02: 4 CI runs." "$out"
# accounts on: 101's flaky attempt and its passing re-run, and 104's expired log.
contains "report: accounts on, the re-run counted as its own job" \
  "accounts on 3 1 0 1 33.3% 0 1" "$out"
contains "report: accounts off" "accounts off 2 1 1 0 0.0% 0 0" "$out"
contains "report: setup failures and cancellations on their own line" \
  "  accounts off: failed before the smoke step 1 (Build the API 1); cancelled 1" "$out"
contains "report: the flaky test with its step" \
  "accounts on 1 0 sign-in.spec.ts › a signed-in visitor on /login goes straight to ?next= flows.ts:102: gave up: sign-in attempt 3 stuck (frame.waitForFunction: Timeout 10000ms exceeded.; frames: http:... (1)" \
  "$out"
contains "report: a failed test" \
  "accounts off 0 1 a11y.spec.ts › dark scheme › a bill's Text tab with its companion bills open is accessible (dark) a11y.spec.ts:82: locator.click: Test timeout of 30000ms exceeded. (1)" \
  "$out"
check "report: the timed-out job list was retried, the gone log wasn't" "2 1" \
  "$(grep -c '/runs/101/jobs' "$tmp/state/calls") $(grep -c '/jobs/1041/logs' "$tmp/state/calls")"

rm -f "$tmp/state/101"
out=$(run --since 2026-10-01 --until 2026-10-01 --no-web-changes)
contains "--no-web-changes: PR 11's run is left out, and said so" \
  "Left out: 1 runs of PRs that change web/, 0 runs with no PR found, 0 runs whose PR's files couldn't be read." "$out"
contains "--no-web-changes: 3 runs, the merged PR found by commit" \
  "Web E2E on pull requests, 2026-10-01 to 2026-10-01: 3 CI runs on PRs that change nothing under web/." "$out"
contains "--no-web-changes: accounts off keeps only 101's pass" "accounts off 1 1 0 0 0.0% 0 0" "$out"
if grep -q "failed before the smoke step 1" <<<"$out"; then
  echo "FAIL --no-web-changes: PR 11's setup failure is still counted"; failures=$((failures + 1))
else echo "ok   --no-web-changes: PR 11's setup failure isn't counted"; fi

out=$(run --since 2026-10-02 --until 2026-10-02); rc=$?
check "a window with no runs: exit 0" "0" "$rc"
contains "a window with no runs: 0 runs reported" "Web E2E on pull requests, 2026-10-02 to 2026-10-02: 0 CI runs." "$out"

check "bad dates are refused" "e2e-flakes: --since 2026-10-03 is after --until 2026-10-01" \
  "$(run --since 2026-10-03 --until 2026-10-01)"

if ((failures)); then echo "$failures failed"; exit 1; fi
echo "all passed"
