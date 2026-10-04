#!/usr/bin/env bash
# Tests scripts/speed-smoke.sh against stand-ins for curl and sleep: the curl stub answers each URL
# from a table (status, a time per request, a body) and records what it was asked; the sleep stub
# records the pacing without waiting. Needs bash, jq and awk.
# Run: scripts/speed-smoke-test.sh
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
script=$here/speed-smoke.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0
check() { # check <name> <expected> <actual>
  if [[ $2 == "$3" ]]; then echo "ok   $1"; else
    echo "FAIL $1:"; diff <(echo "$2") <(echo "$3") | sed 's/^/     /'; failures=$((failures + 1))
  fi
}
squeeze() { tr -s ' ' | sed 's/^ //'; }
contains() { # contains <name> <line> <text>: the text has the line, spaces squeezed and leading ones dropped
  if squeeze <<<"$3" | grep -qxF -- "$(squeeze <<<"$2")"; then echo "ok   $1"; else
    echo "FAIL $1: no line [$2] in:"; printf '%s\n' "$3" | sed 's/^/     /'; failures=$((failures + 1))
  fi
}
lacks() { # lacks <name> <text> <haystack>
  if grep -qF -- "$2" <<<"$3"; then echo "FAIL $1: found [$2]"; failures=$((failures + 1)); else echo "ok   $1"; fi
}

mkdir -p "$tmp/bin"
# curl: answers from $STUB/routes, lines "<path>\t<status>\t<seconds,...>\t<body>", the times used
# in turn per path (the last one repeats); any other path is 200 in 10 ms with {}. STUB_DOWN=1
# refuses every connection. Records each call's arguments, its URL and the header file it was given.
cat >"$tmp/bin/curl" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB/argv"
out="" url=""
while (($#)); do
  case $1 in
    -o) out=$2; shift 2 ;;
    -w | --max-time) shift 2 ;;
    -H) [[ $2 == @* ]] && cat "${2#@}" >>"$STUB/headers"; shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
printf '%s\n' "$url" >>"$STUB/calls"
if [[ -n ${STUB_DOWN:-} ]]; then
  echo "curl: (7) Failed to connect to localhost port 1: Connection refused" >&2
  printf '000 0.000000'
  exit 7
fi
path=${url#*/api/v1}
spec=$(awk -F'\t' -v p="$path" '$1 == p' "$STUB/routes")
status=200 times=0.010 body='{}'
[[ -z $spec ]] || IFS=$'\t' read -r _ status times body <<<"$spec"
key=$(printf '%s' "$path" | md5sum | cut -c1-12)
i=$(cat "$STUB/count-$key" 2>/dev/null || echo 0)
echo $((i + 1)) >"$STUB/count-$key"
IFS=, read -ra list <<<"$times"
t=${list[i]:-${list[-1]}}
printf '%s' "$body" >"$out"
printf '%s %s' "$status" "$t"
STUB
# shellcheck disable=SC2016 # expanded when the stub runs
printf '#!/usr/bin/env bash\necho "$1" >>"$STUB/sleeps"\n' >"$tmp/bin/sleep"
chmod +x "$tmp/bin/curl" "$tmp/bin/sleep"

T=$'\t'
base_routes() {
  cat <<ROUTES
/congresses${T}200${T}0.010${T}[{"number":118,"is_current":false},{"number":119,"is_current":true}]
/bills${T}200${T}0.010${T}{"items":[{"id":"hr-119-808"}],"total":1}
/bills/hr-119-808${T}200${T}0.900,0.100,0.200,0.700${T}{"bill":{},"text_versions":[{"id":"v1"}],"diffs":[{"id":"d1"}]}
/bills/hr-119-808/related${T}200${T}0.100,1.900,1.000,1.500${T}{"related":[]}
/bills/hr-119-808/law-changes${T}200${T}0.010${T}{"changes":[{"section_id":"/us/usc/t42/s1395w-4"}]}
/bills/hr-119-808/aggregates${T}404${T}0.010${T}{"code":"aggregates_off"}
/members${T}200${T}0.010${T}{"items":[{"bioguide_id":"A000001"}],"total":1}
/members/A000001/alignment${T}404${T}0.010${T}{"code":"aggregates_off"}
ROUTES
}

# run <case> [args...]: runs the script against the stub with $tmp/<case>/routes; sets out, rc.
run() {
  export STUB=$tmp/$1
  shift
  out=$(PATH="$tmp/bin:$PATH" "$script" "$@" 2>&1)
  rc=$?
}

# --- a full run: every route timed, the slow one marked, the optional ones skipped ---
mkdir -p "$tmp/ok" && base_routes >"$tmp/ok/routes"
run ok http://api.test/ --n 4
check "a run with nothing failed exits 0" 0 "$rc"
# Cold 900; requests 2 to 4 took 100, 200 and 700 ms: p50 is the 2nd of 3 (200), p95 the 3rd (700).
contains "the bill detail's cold, p50 and p95, over its 500 ms budget" \
  "/bills/{id} 500 900 200 700 OVER" "$out"
# 1900, 1000, 1500: p50 1500 and p95 1900, under the graph routes' 2 s.
contains "a graph route is judged against 2 s" "/bills/{id}/related 2000 100 1500 1900 " "$out"
contains "a fast route" "/congresses 500 10 10 10 " "$out"
contains "a feature that's off is skipped, not failed" \
  "/bills/{id}/aggregates: 404, aggregates aren't public here" "$out"
calls=$(sort -u "$tmp/ok/calls")
for url in /bills?congress=119 /bills/counts?congress=119 /bill-index?congress=119 \
  /bill-statuses?congress=119 /bills/hr-119-808/text/v1 /bills/hr-119-808/diffs/d1 /law/42/1395w-4 \
  /members/A000001 /members/A000001/positions?congress=119 /members/A000001/collaborators; do
  contains "requests $url, with IDs found in earlier answers" "http://api.test/api/v1$url" "$calls"
done
check "each timed route is asked --n times" 4 "$(grep -cxF 'http://api.test/api/v1/bills/hr-119-808/votes' "$tmp/ok/calls")"
check "every request but the first waits" "$(($(wc -l <"$tmp/ok/calls") - 1))" "$(wc -l <"$tmp/ok/sleeps")"
check "1 s, without a server key" 1.000 "$(sort -u "$tmp/ok/sleeps")"

run ok http://api.test/api/v1 --n 4 --strict
check "--strict fails a run with a route over budget" 1 "$rc"
contains "--strict says how many" "1 route(s) over budget (--strict)." "$out"

# --- a route answering an error: named, the rest still timed, exit 1 ---
mkdir -p "$tmp/err" && base_routes >"$tmp/err/routes"
echo "/bills/hr-119-808/votes${T}500${T}0.010${T}{\"error\":\"failed\"}" >>"$tmp/err/routes"
run err http://api.test --n 3
check "a route that answers 500 fails the run" 1 "$rc"
contains "and is named" "/bills/{id}/votes: request 1 of 3 answered 500" "$out"
contains "the other routes are still timed" "/members/{id} 500 10 10 10 " "$out"

mkdir -p "$tmp/limited" && base_routes >"$tmp/limited/routes"
echo "/policy-areas${T}429${T}0.010${T}{}" >>"$tmp/limited/routes"
run limited http://api.test --n 3
contains "a 429 says how to get under the limit" \
  "/policy-areas: request 1 of 3 answered 429 (rate limited: lower --rate, or set API_SERVER_KEY to one of the API's API_SERVER_KEYS)" \
  "$out"

# --- the base URL doesn't answer ---
mkdir -p "$tmp/down" && : >"$tmp/down/routes"
STUB_DOWN=1 run down http://localhost:1
check "an unreachable base URL exits 2" 2 "$rc"
check "and says so" \
  "speed-smoke: can't reach http://localhost:1: curl: (7) Failed to connect to localhost port 1: Connection refused" \
  "$(tail -1 <<<"$out")"
check "after one try" 1 "$(wc -l <"$tmp/down/calls")"

# --- the server key: 2 a second, sent from a file ---
mkdir -p "$tmp/key" && base_routes >"$tmp/key/routes"
API_SERVER_KEY=not-a-real-key-0123456789abcdef run key http://api.test --n 2
check "with a server key it runs at 2 a second" 0.500 "$(sort -u "$tmp/key/sleeps")"
check "and sends the key as X-Server-Key" "X-Server-Key: not-a-real-key-0123456789abcdef" "$(sort -u "$tmp/key/headers")"
lacks "never on the command line" "not-a-real-key" "$(cat "$tmp/key/argv")"

# --- bad usage ---
for args in "" "--n 1 http://api.test" "--rate 3 http://api.test" "--law 42 http://api.test" "api.test"; do
  mkdir -p "$tmp/usage" && : >"$tmp/usage/routes"
  # shellcheck disable=SC2086 # the words are the arguments
  run usage $args
  check "bad usage exits 2: [$args]" 2 "$rc"
done
check "and sends nothing" "" "$(cat "$tmp/usage/calls" 2>/dev/null)"

if ((failures)); then
  echo "$failures check(s) failed"
  exit 1
fi
echo "all checks passed"
