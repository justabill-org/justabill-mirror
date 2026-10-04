#!/usr/bin/env bash
# Times the API's public GET routes against a running API (#873): each route N times, paced, with
# p50 and p95 per route against the performance budget (500 ms, 2 s for the graph routes; the
# performance-cost skill). Read-only and signed out. Numbers from a local stack say nothing about
# production, and only the maintainer runs it against production.
#
# Usage: scripts/speed-smoke.sh <base-url> [--n N] [--rate R] [--strict]
#                               [--bill ID] [--member ID] [--law TITLE/SECTION]
#   <base-url>  the API's origin, e.g. http://localhost:8080 (a trailing /api/v1 is fine)
#   --n N       requests per route, the first of them cold (default 20, at least 2)
#   --rate R    requests a second, at most 2 (default 2 with API_SERVER_KEY, else 1: the public
#               limit is 60 a minute per IP, and the web server's key has a bucket of its own)
#   --strict    exit 1 when a route's p95 is over its budget
#   --bill, --member, --law  what the per-bill, per-member and US Code routes read (default: the
#               first bill and member the lists return, and a section the bill's law changes name)
# API_SERVER_KEY, if set, is sent as X-Server-Key (from a file, never on curl's command line).
#
# Each route's first request is "cold": a cache miss unless something asked for the same URL within
# its cache TTL. p50 and p95 are of the rest (nearest rank). Times are curl's time_total in ms.
# Exit: 0 done; 1 a route answered other than 200, didn't answer, or (--strict) was over budget;
# 2 bad usage, a missing tool, or the base URL unreachable.
set -euo pipefail

usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2; exit 2; }
die() { echo "speed-smoke: $*" >&2; exit 2; }

budget_ms=500
graph_budget_ms=2000
n=20 rate="" strict=0 bill="" member="" law="" base=""
while (($#)); do
  case $1 in
    --n) n=${2:-}; shift 2 ;;
    --rate) rate=${2:-}; shift 2 ;;
    --strict) strict=1; shift ;;
    --bill) bill=${2:-}; shift 2 ;;
    --member) member=${2:-}; shift 2 ;;
    --law) law=${2:-}; shift 2 ;;
    -h | --help) usage ;;
    -*) die "unknown option $1 (--help lists them)" ;;
    *) [[ -z $base ]] || die "one base URL only"; base=$1; shift ;;
  esac
done
[[ -n $base ]] || usage
[[ $base =~ ^https?:// ]] || die "the base URL must start with http:// or https://: $base"
if ! [[ $n =~ ^[0-9]+$ ]] || ((n < 2)); then die "--n must be a whole number, at least 2"; fi
[[ -n $rate ]] || { [[ -n ${API_SERVER_KEY:-} ]] && rate=2 || rate=1; }
awk -v r="$rate" 'BEGIN { exit !(r + 0 > 0 && r + 0 <= 2) }' || die "--rate must be over 0 and at most 2"
[[ -z $law || $law =~ ^[0-9]+[a-zA-Z]?/[^/]+$ ]] || die "--law must be TITLE/SECTION, e.g. 42/1395w-4"
for tool in curl jq awk; do command -v "$tool" >/dev/null || die "needs $tool"; done

base=${base%/}
base=${base%/api/v1}
api=$base/api/v1
interval=$(awk -v r="$rate" 'BEGIN { printf "%.3f", 1 / r }')

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
headers=()
if [[ -n ${API_SERVER_KEY:-} ]]; then
  (umask 077 && printf 'X-Server-Key: %s\n' "$API_SERVER_KEY" >"$tmp/headers")
  headers=(-H "@$tmp/headers")
fi

rows=() failures=() skipped=() over=0 sent=0

# get <path>: one paced request; sets code and ms, the body in $tmp/body. Returns curl's status.
get() {
  ((sent++ == 0)) || sleep "$interval"
  local out rc=0
  out=$(curl -sS -o "$tmp/body" -w '%{http_code} %{time_total}' --max-time 30 "${headers[@]}" \
    "$api$1" 2>"$tmp/err") || rc=$?
  code=${out%% *}
  ms=$(awk -v s="${out##* }" 'BEGIN { printf "%d", s * 1000 + 0.5 }')
  return "$rc"
}

# refused <label> <code> <request>: records a route that answered other than 200.
refused() {
  local hint=""
  [[ $2 == 429 ]] && hint=" (rate limited: lower --rate, or set API_SERVER_KEY to one of the API's API_SERVER_KEYS)"
  failures+=("$1: request $3 of $n answered $2$hint")
}

# percentile <p> <file of ms>: the nearest-rank percentile.
percentile() {
  sort -n "$2" | awk -v p="$1" '{ v[NR] = $1 } END { i = int(p * NR / 100); if (i < p * NR / 100) i++; print v[i] }'
}

# time_route <label> <budget ms> <path> [skip-404-because]: times path n times, the first body kept
# in $tmp/first. A route given a reason may answer 404 (a feature that's off): it's skipped, not failed.
time_route() {
  local label=$1 budget=$2 path=$3 reason=${4:-} cold i
  if ! get "$path"; then
    ((sent > 1)) || die "can't reach $base: $(tr -d '\n' <"$tmp/err")"
    failures+=("$label: no answer ($(tr -d '\n' <"$tmp/err"))")
    return 1
  fi
  if [[ $code == 404 && -n $reason ]]; then
    skipped+=("$label: 404, $reason")
    return 1
  fi
  [[ $code == 200 ]] || { refused "$label" "$code" 1; return 1; }
  cold=$ms
  cp "$tmp/body" "$tmp/first"
  : >"$tmp/times"
  for ((i = 2; i <= n; i++)); do
    get "$path" || { failures+=("$label: request $i of $n got no answer ($(tr -d '\n' <"$tmp/err"))"); return 1; }
    [[ $code == 200 ]] || { refused "$label" "$code" "$i"; return 1; }
    echo "$ms" >>"$tmp/times"
  done
  local p50 p95 mark=""
  p50=$(percentile 50 "$tmp/times")
  p95=$(percentile 95 "$tmp/times")
  if ((p95 > budget)); then mark="OVER"; over=$((over + 1)); fi
  rows+=("$(printf '%-34s %6s %6s %6s %6s  %s' "$label" "$budget" "$cold" "$p50" "$p95" "$mark")")
}

# first <jq filter>: the first value the filter finds in the last timed route's first body.
first() { jq -r "[$1] | map(select(. != null and . != \"\")) | first // empty" "$tmp/first" 2>/dev/null || true; }

echo "Timing $api: $n requests a route, $rate a second."

congress=""
if time_route "/congresses" "$budget_ms" "/congresses"; then
  congress=$(first '.[] | select(.is_current) | .number')
  [[ -n $congress ]] || congress=$(first '.[].number')
fi
time_route "/policy-areas" "$budget_ms" "/policy-areas" || true
if time_route "/bills" "$budget_ms" "/bills"; then
  [[ -n $bill ]] || bill=$(first '.items[]?.id')
fi
if [[ -n $congress ]]; then
  time_route "/bills?congress" "$budget_ms" "/bills?congress=$congress" || true
  time_route "/bills/counts?congress" "$budget_ms" "/bills/counts?congress=$congress" || true
  time_route "/bill-index?congress" "$budget_ms" "/bill-index?congress=$congress" || true
  time_route "/bill-statuses?congress" "$budget_ms" "/bill-statuses?congress=$congress" || true
else
  skipped+=("the per-congress routes: /congresses named no congress")
fi

if [[ -n $bill ]]; then
  b=/bills/$(jq -rn --arg v "$bill" '$v | @uri')
  vid="" did=""
  if time_route "/bills/{id}" "$budget_ms" "$b"; then
    vid=$(first '.text_versions[]?.id')
    did=$(first '.diffs[]?.id')
  fi
  for sub in actions votes text diffs amendments gao-reports; do
    time_route "/bills/{id}/$sub" "$budget_ms" "$b/$sub" || true
  done
  time_route "/bills/{id}/related" "$graph_budget_ms" "$b/related" || true
  time_route "/bills/{id}/companion-votes" "$graph_budget_ms" "$b/companion-votes" || true
  if time_route "/bills/{id}/law-changes" "$budget_ms" "$b/law-changes" && [[ -z $law ]]; then
    law=$(first '.changes[]?.section_id | capture("^/us/usc/t(?<t>[0-9]+[a-z]?)/s(?<s>[^/]+)$")? | "\(.t)/\(.s)"')
  fi
  time_route "/bills/{id}/aggregates" "$budget_ms" "$b/aggregates" "aggregates aren't public here" || true
  if [[ -n $vid ]]; then
    time_route "/bills/{id}/text/{vid}" "$budget_ms" "$b/text/$vid" "the bill's first version has no stored text" || true
  else
    skipped+=("/bills/{id}/text/{vid}: $bill has no text versions")
  fi
  if [[ -n $did ]]; then
    time_route "/bills/{id}/diffs/{did}" "$budget_ms" "$b/diffs/$did" || true
  else
    skipped+=("/bills/{id}/diffs/{did}: $bill has no diffs")
  fi
else
  skipped+=("the per-bill routes: no bill (the list is empty; pass --bill)")
fi
if [[ -n $law ]]; then
  time_route "/law/{title}/{section}" "$budget_ms" "/law/${law%%/*}/$(jq -rn --arg v "${law#*/}" '$v | @uri')" || true
else
  skipped+=("/law/{title}/{section}: the bill's law changes name no US Code section (pass --law)")
fi

if time_route "/members" "$budget_ms" "/members" && [[ -z $member ]]; then
  member=$(first '.items[]?.bioguide_id')
fi
if [[ -n $member ]]; then
  m=/members/$(jq -rn --arg v "$member" '$v | @uri')
  time_route "/members/{id}" "$budget_ms" "$m" || true
  if [[ -n $congress ]]; then
    time_route "/members/{id}/positions?congress" "$budget_ms" "$m/positions?congress=$congress" || true
  else
    skipped+=("/members/{id}/positions: /congresses named no congress")
  fi
  time_route "/members/{id}/collaborators" "$graph_budget_ms" "$m/collaborators" || true
  time_route "/members/{id}/alignment" "$budget_ms" "$m/alignment" "aggregates aren't public here" || true
else
  skipped+=("the per-member routes: no member (the list is empty; pass --member)")
fi

echo
printf '%-34s %6s %6s %6s %6s\n' "route" "budget" "cold" "p50" "p95"
printf '%s\n' "${rows[@]}"
echo "(ms; p50 and p95 of requests 2 to $n; OVER: p95 over the budget)"
if ((${#skipped[@]})); then
  echo
  echo "Not timed:"
  printf '  %s\n' "${skipped[@]}"
fi
if ((${#failures[@]})); then
  echo
  echo "Failed:"
  printf '  %s\n' "${failures[@]}"
  exit 1
fi
if ((strict && over)); then
  echo
  echo "$over route(s) over budget (--strict)."
  exit 1
fi
exit 0
