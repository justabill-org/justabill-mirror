#!/usr/bin/env bash
# Web E2E flake report (#777, #792): every CI run on a pull request in a window, its Web E2E suite
# jobs (each re-run attempt counts as its own job), and the Playwright summary in the log of each job
# whose smoke-test step failed. Prints, per suite, the jobs that reached the smoke step, real failures
# (a test failed on its retry too) and flake-only failures (a test passed only on its retry, which
# failOnFlakyTests turns red), then the flaky and failed tests by spec › test with their failing step.
#
#   scripts/e2e-flakes.sh [--since YYYY-MM-DD] [--until YYYY-MM-DD] [--no-web-changes]
#
#   --since, --until   the UTC days the runs were created on, inclusive (default: the last 14 days)
#   --no-web-changes   only PRs that change no file under web/ (their files as the PR has them now)
#
# Needs gh (signed in, with read access to Actions) and jq. Jobs that failed before the smoke step
# or were cancelled are on a line of their own: they're the runners, and no spec fix moves them. API
# calls are retried with backoff, and whatever still can't be read (a job list, an expired log) is
# counted in the report, never dropped. Env: E2E_FLAKES_REPO (default justabill-org/justabill),
# E2E_FLAKES_PARALLEL (8 runs at a time), E2E_FLAKES_RETRIES (4 tries a call), E2E_FLAKES_RECORDS
# (a file to keep the records in, to find the jobs behind a number).
#
# Two internal subcommands, which scripts/e2e-flakes-test.sh runs against saved log excerpts:
#   e2e-flakes.sh classify <job.json> [<log>]   the records for one job (format below)
#   e2e-flakes.sh report <records>              the tables for a file of records
# Records are tab-separated lines:
#   J <suite> <job id> <pass|real|flaky|other|unreadable|setup|cancelled> <detail>
#   T <suite> <job id> <flaky|failed> <spec › test> <failing step>
#   U run <run id>          a run whose job list couldn't be read
#   M <key> <value>         window, runs, filter: what the header says
set -euo pipefail

REPO=${E2E_FLAKES_REPO:-justabill-org/justabill}
PARALLEL=${E2E_FLAKES_PARALLEL:-8}
RETRIES=${E2E_FLAKES_RETRIES:-4}
self=$(cd "$(dirname "$0")" && pwd)/$(basename "$0")

die() { echo "e2e-flakes: $*" >&2; exit 2; }

# with_timeout <cmd...>: a 120 s limit, with coreutils timeout (gtimeout on macOS) when there is one.
with_timeout() {
  if command -v timeout >/dev/null; then timeout 120 "$@"
  elif command -v gtimeout >/dev/null; then gtimeout 120 "$@"
  else "$@"
  fi
}

# api <out> <gh api args...>: retries timeouts and server errors with backoff. Returns 2 when the
# resource is gone (a log past its retention: 404 or 410), 1 when every try failed.
api() {
  local out=$1 err try
  shift
  err=$(mktemp)
  for ((try = 1; try <= RETRIES; try++)); do
    if with_timeout gh api "$@" >"$out" 2>"$err"; then
      rm -f "$err"
      return 0
    fi
    if grep -qE 'HTTP (404|410)' "$err"; then
      rm -f "$err"
      return 2
    fi
    ((try < RETRIES)) && sleep $((2 ** try))
  done
  rm -f "$err"
  return 1
}

# The Playwright list reporter's output, as it appears in a GitHub Actions log. Each line carries a
# timestamp; a line without one continues an annotation (##[error], ##[notice]) that repeats output,
# so those are skipped. A failed test's report starts "  1) [chromium] › e2e/x.spec.ts:56:5 ›
# title", and its first "Error:" line and first e2e/ stack frame are the failing step (the first
# attempt's, when a "Retry #1" report follows). The final summary is "  N failed|flaky|..." with
# one indented "[chromium] › ..." line per test. Tests are named "x.spec.ts › title", without line numbers, so
# one test keeps one name across commits. Output: the class, then "<kind>\t<test>\t<step>" lines.
# shellcheck disable=SC2016 # an awk program
parse_awk='
function norm(t) {
  # A short title is padded with a rule ("─") in the summary.
  sub(/^e2e\//, "", t); sub(/:[0-9]+:[0-9]+ /, " ", t); sub(/( |─)+$/, "", t)
  return t
}
function finish() {
  if (cur != "" && !(cur in step)) {
    m = msg != "" ? msg : first
    if (length(m) > 100) m = substr(m, 1, 97) "..."
    step[cur] = (loc != "" ? loc ": " : "") (m != "" ? m : "?")
  }
  cur = ""; msg = ""; first = ""; loc = ""
}
{
  sub(/\r$/, ""); gsub(esc "\\[[0-9;]*[A-Za-z]", "")
  if ($0 !~ /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9:.]*Z /) next
  line = substr($0, index($0, " ") + 1)
  if (match(line, /^  [0-9]+\) \[[^ ]*\] › /)) { finish(); cur = norm(substr(line, RLENGTH + 1)); section = ""; next }
  if (match(line, /^  [0-9]+ (failed|flaky|interrupted|skipped|passed|did not run)/)) {
    finish(); summary = 1
    n = line; sub(/^  /, "", n); split(n, w, " "); section = substr(n, length(w[1]) + 2)
    sub(/ \(.*$/, "", section); count[section] += w[1]
    next
  }
  if (section != "" && match(line, /^    \[[^ ]*\] › /)) {
    if (section == "failed" || section == "interrupted" || section == "flaky") {
      t = norm(substr(line, RLENGTH + 1)); kind = section == "flaky" ? "flaky" : "failed"
      if (!((kind, t) in seen)) { seen[kind, t] = 1; order[++tests] = kind "\t" t }
    }
    next
  }
  if (cur == "") next
  if (msg == "" && match(line, /^    [A-Za-z]*Error: /)) { msg = substr(line, 5); sub(/^Error: /, "", msg) }
  else if (first == "" && line ~ /^    [^ ]/ && line !~ /^    (Call log|Error Context|attachment)/)
    first = substr(line, 5)
  if (loc == "" && match(line, /\/e2e\/[^:) ]+:[0-9]+/)) loc = substr(line, RSTART + 5, RLENGTH - 5)
}
END {
  finish()
  if (count["failed"] + count["interrupted"] > 0) print "real"
  else if (count["flaky"] > 0) print "flaky"
  else if (summary) print "other\tno failed or flaky test in the summary"
  else print "other\tno Playwright summary"
  for (i = 1; i <= tests; i++) {
    split(order[i], kt, "\t")
    print order[i] "\t" (kt[2] in step ? step[kt[2]] : "?")
  }
}'

# classify <job.json> [<log>]: the records for one Web E2E suite job (a GitHub Actions job object).
classify() {
  local job=$1 log=${2:-} suite id conclusion smoke failed out class detail
  IFS=$'\t' read -r suite id conclusion smoke failed < <(jq -r '[
      (if .name == "Web E2E" then "one job" else (.name | capture("^Web E2E \\((?<s>.*)\\)$").s) end),
      .id, (.conclusion // "none"),
      ([.steps[] | select((.name | startswith("Smoke tests")) and .conclusion != "skipped")
        | .conclusion][0] // "none"),
      # A step that runs an action names its commit: "Run actions/checkout@<sha>" reads as actions/checkout.
      ([.steps[] | select(.conclusion == "failure") | .name | sub("^Run (?<a>[^@ ]+)@[0-9a-f]{40}$"; "\(.a)")][0]
        // "no step failed")
    ] | map(tostring) | @tsv' "$job")
  if [[ $conclusion == cancelled || $smoke == cancelled ]]; then
    printf 'J\t%s\t%s\tcancelled\t\n' "$suite" "$id"
  elif [[ $smoke == success ]]; then
    printf 'J\t%s\t%s\tpass\t\n' "$suite" "$id"
  elif [[ $smoke != failure ]]; then
    printf 'J\t%s\t%s\tsetup\t%s\n' "$suite" "$id" "$failed"
  elif [[ -z $log || ! -s $log ]]; then
    printf 'J\t%s\t%s\tunreadable\t%s\n' "$suite" "$id" "${E2E_FLAKES_LOG_ERROR:-no log}"
  else
    out=$(LC_ALL=C awk -v esc=$'\033' "$parse_awk" "$log")
    IFS=$'\t' read -r class detail <<<"$(head -n 1 <<<"$out")"
    printf 'J\t%s\t%s\t%s\t%s\n' "$suite" "$id" "$class" "${detail:-}"
    tail -n +2 <<<"$out" | while IFS=$'\t' read -r kind test step; do
      printf 'T\t%s\t%s\t%s\t%s\t%s\n' "$suite" "$id" "$kind" "$test" "$step"
    done
  fi
}

# worker <records dir> <run id>: fetches the run's jobs from every attempt and the log of each suite
# job whose smoke step failed, and writes their records to <records dir>/<run id>. Workers run in
# parallel, so each writes a file of its own: concurrent cats into one shared descriptor lose lines,
# because cat copies with copy_file_range or splice, which don't lock the file offset (#777).
worker() {
  local dir
  dir=$(mktemp -d)
  run_records "$2" "$dir" >"$1/$2"
  rm -rf "$dir"
}

run_records() { # run_records <run id> <scratch dir>
  local run=$1 dir=$2 job log rc
  if ! api "$dir/jobs.json" --paginate "repos/$REPO/actions/runs/$run/jobs?filter=all&per_page=100"; then
    printf 'U\trun\t%s\n' "$run"
    return
  fi
  jq -c '.jobs[] | select((.name == "Web E2E" or (.name | startswith("Web E2E ("))) and
      any(.steps[]; .name | startswith("Smoke tests")))' "$dir/jobs.json" >"$dir/suites.jsonl"
  while IFS= read -r job; do
    printf '%s\n' "$job" >"$dir/job.json"
    log=""
    if jq -e '.conclusion != "cancelled" and any(.steps[]; (.name | startswith("Smoke tests")) and
        .conclusion == "failure")' "$dir/job.json" >/dev/null; then
      log=$dir/job.log
      rc=0
      api "$log" --allow-escape-sequences "repos/$REPO/actions/jobs/$(jq -r .id "$dir/job.json")/logs" || rc=$?
      case $rc in
        0) ;;
        2) : >"$log"; export E2E_FLAKES_LOG_ERROR="log expired" ;;
        *) : >"$log"; export E2E_FLAKES_LOG_ERROR="log unreadable after $RETRIES tries" ;;
      esac
    fi
    classify "$dir/job.json" "$log"
    unset E2E_FLAKES_LOG_ERROR
  done <"$dir/suites.jsonl"
}

# report <records>: the tables.
report() {
  local records=$1 tab=$'\t'
  awk -F'\t' '
    $1 == "M" { meta[$2] = $3 }
    $1 == "U" { unreadable_runs++ }
    $1 == "J" && $4 == "unreadable" { unreadable_logs++ }
    END {
      printf "Web E2E on pull requests, %s to %s: %d CI runs%s.\n", meta["since"], meta["until"], meta["runs"],
        meta["filter"] != "" ? " " meta["filter"] : ""
      if (meta["excluded"] != "") printf "%s\n", meta["excluded"]
      printf "Re-run attempts count as their own jobs. Couldn'"'"'t read: %d runs'"'"' job lists, %d job logs.\n\n",
        unreadable_runs, unreadable_logs
    }' "$records"
  {
    printf '%s\t' Suite "Reached the smoke step" Passed "Real failure" "Flake only" "Flake rate" Other
    echo "Log unreadable"
    awk -F'\t' '
      $1 == "J" { suites[$2] = 1; n[$2, $4]++ }
      END {
        for (s in suites) {
          reached = n[s, "pass"] + n[s, "real"] + n[s, "flaky"] + n[s, "other"] + n[s, "unreadable"]
          rate = reached ? sprintf("%.1f%%", 100 * n[s, "flaky"] / reached) : "-"
          printf "%s\t%d\t%d\t%d\t%d\t%s\t%d\t%d\n", s, reached, n[s, "pass"], n[s, "real"], n[s, "flaky"], rate,
            n[s, "other"], n[s, "unreadable"]
        }
      }' "$records" | sort
  } | column -t -s "$tab"
  echo
  echo "Not counted above (runner setup or cancelled; no spec fix moves them):"
  awk -F'\t' '
    $1 == "J" { suites[$2] = 1; n[$2, $4]++ }
    $1 == "J" && $4 == "setup" && !setup[$2, $5]++ { steps[$2] = steps[$2] "\t" $5 }
    END {
      for (s in suites) {
        detail = ""
        k = split(substr(steps[s], 2), st, "\t")
        for (i = 1; i <= k; i++) detail = detail (i > 1 ? ", " : "") st[i] " " setup[s, st[i]]
        printf "  %s: failed before the smoke step %d%s; cancelled %d\n", s, n[s, "setup"],
          detail != "" ? " (" detail ")" : "", n[s, "cancelled"]
      }
    }' "$records" | sort
  echo
  echo "Flaky and failed tests by spec › test (a job can list more than one):"
  if ! grep -q '^T' "$records"; then
    echo "  none"
    return
  fi
  {
    echo "Suite${tab}Flaky${tab}Failed${tab}Spec › test${tab}Failing step (most common)"
    awk -F'\t' '
      $1 == "T" {
        key = $2 "\t" $5; keys[key] = 1; n[key, $4]++; c = ++steps[key, $6]
        if (c > best[key]) { best[key] = c; top[key] = $6 }
      }
      END {
        for (k in keys) printf "%d\t%d\t%s\t%s (%d)\n", n[k, "flaky"], n[k, "failed"], k, top[k], best[k]
      }' "$records" | sort -t "$tab" -k1,1nr -k2,2nr -k3,4 |
      awk -F'\t' -v OFS='\t' '{ print $3, $1, $2, $4, $5 }'
  } | column -t -s "$tab"
}

next_day() { date -u -d "$1 +1 day" +%F 2>/dev/null || date -u -j -v+1d -f %F "$1" +%F; }
days_ago() { date -u -d "$1 days ago" +%F 2>/dev/null || date -u -v-"$1"d +%F; }

main() {
  local since until no_web=false tmp d runs pr files excluded="" nopr=0 webpr=0 unreadpr=0
  since=$(days_ago 13)
  until=$(date -u +%F)
  while (($#)); do
    case $1 in
      --since) since=${2:?--since needs a date}; shift 2 ;;
      --until) until=${2:?--until needs a date}; shift 2 ;;
      --no-web-changes) no_web=true; shift ;;
      -h | --help) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; return ;;
      *) die "unknown argument $1 (see --help)" ;;
    esac
  done
  [[ $since =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ && $until =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || die "dates are YYYY-MM-DD"
  [[ ! $since > $until ]] || die "--since $since is after --until $until"
  command -v jq >/dev/null || die "needs jq"
  gh auth status >/dev/null 2>&1 || die "needs gh signed in (gh auth login)"

  tmp=$(mktemp -d)
  # shellcheck disable=SC2064 # tmp is local: expand it now
  trap "rm -rf '$tmp'" EXIT
  : >"$tmp/runs.tsv"
  # One query a day: the API returns at most 1,000 runs for a query.
  d=$since
  while [[ ! $d > $until ]]; do
    if api "$tmp/day.tsv" --paginate \
      --jq '.workflow_runs[] | [.id, .head_sha, (.pull_requests[0].number // "")] | @tsv' \
      "repos/$REPO/actions/workflows/ci.yml/runs?event=pull_request&status=completed&created=$d&per_page=100"; then
      cat "$tmp/day.tsv" >>"$tmp/runs.tsv"
    else
      die "couldn't list $d's CI runs after $RETRIES tries; try again later"
    fi
    d=$(next_day "$d")
  done
  sort -u -o "$tmp/runs.tsv" "$tmp/runs.tsv"

  if $no_web; then
    : >"$tmp/kept.tsv"
    while IFS=$'\t' read -r run sha pr; do
      # A PR that has since merged or closed no longer shows on its runs: look it up by commit.
      if [[ -z $pr ]]; then
        if [[ ! -e $tmp/sha-$sha ]]; then
          api "$tmp/sha-$sha" --jq '.[0].number // ""' "repos/$REPO/commits/$sha/pulls" || : >"$tmp/sha-$sha"
        fi
        pr=$(cat "$tmp/sha-$sha")
      fi
      if [[ -z $pr ]]; then
        nopr=$((nopr + 1))
        continue
      fi
      files=$tmp/pr-$pr
      if [[ ! -e $files ]] &&
        ! api "$files" --paginate --jq '.[].filename' "repos/$REPO/pulls/$pr/files?per_page=100"; then
        echo "unreadable" >"$files"
      fi
      if grep -qx unreadable "$files"; then
        unreadpr=$((unreadpr + 1))
      elif grep -q '^web/' "$files"; then
        webpr=$((webpr + 1))
      else
        printf '%s\t%s\t%s\n' "$run" "$sha" "$pr" >>"$tmp/kept.tsv"
      fi
    done <"$tmp/runs.tsv"
    excluded="Left out: $webpr runs of PRs that change web/, $nopr runs with no PR found,"
    excluded+=" $unreadpr runs whose PR's files couldn't be read."
    mv "$tmp/kept.tsv" "$tmp/runs.tsv"
  fi
  runs=$(wc -l <"$tmp/runs.tsv" | tr -d ' ')

  {
    printf 'M\tsince\t%s\nM\tuntil\t%s\nM\truns\t%s\n' "$since" "$until" "$runs"
    $no_web && printf 'M\tfilter\ton PRs that change nothing under web/\nM\texcluded\t%s\n' "$excluded"
    # GNU xargs runs the command once on empty input, so only start workers when there are runs.
    if ((runs > 0)); then
      mkdir "$tmp/records"
      cut -f1 "$tmp/runs.tsv" | E2E_FLAKES_REPO=$REPO E2E_FLAKES_RETRIES=$RETRIES \
        xargs -P "$PARALLEL" -n 1 "$self" worker "$tmp/records"
      cat "$tmp/records"/*
    fi
  } >"$tmp/records.tsv"
  [[ -n ${E2E_FLAKES_RECORDS:-} ]] && cp "$tmp/records.tsv" "$E2E_FLAKES_RECORDS"
  report "$tmp/records.tsv"
}

case ${1:-} in
  classify) shift; classify "$@" ;;
  report) shift; report "$@" ;;
  worker) shift; worker "$@" ;;
  *) main "$@" ;;
esac
