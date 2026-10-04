#!/usr/bin/env bash
# Tests scripts/infra.sh with a stand-in for docker that records its arguments and exits with
# $DOCKER_EXIT. Run: scripts/infra-test.sh
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0
check() { # check <name> <expected> <actual>
  if [[ $2 == "$3" ]]; then echo "ok   $1"; else echo "FAIL $1: expected [$2], got [$3]"; failures=$((failures + 1)); fi
}

mkdir "$tmp/bin"
cat >"$tmp/bin/docker" <<'SH'
#!/usr/bin/env bash
echo "$*" >>"$DOCKER_LOG"
exit "${DOCKER_EXIT:-0}"
SH
chmod +x "$tmp/bin/docker"
export PATH="$tmp/bin:$PATH" DOCKER_LOG="$tmp/docker.log"

# run <JAB_IN_CONTAINER> <DOCKER_EXIT> <args...>: prints "rc=<status> docker=[<calls>]"
run() {
  local in_container=$1 docker_exit=$2
  shift 2
  : >"$DOCKER_LOG"
  JAB_IN_CONTAINER=$in_container DOCKER_EXIT=$docker_exit "$here/infra.sh" "$@" >/dev/null 2>&1
  local rc=$?
  echo "rc=$rc docker=[$(tr '\n' ';' <"$DOCKER_LOG")]"
}

# On the host: the same compose commands task infra:up and task migrate ran before.
check "host up starts the three services" \
  "rc=0 docker=[compose up -d spanner-emulator auth-emulator redis;]" "$(run "" 0 up)"
check "host init runs spanner-init" "rc=0 docker=[compose run --rm spanner-init;]" "$(run "" 0 init)"
check "host init exits with spanner-init's status" \
  "rc=3 docker=[compose run --rm spanner-init;]" "$(run "" 3 init)"
check "JAB_IN_CONTAINER=0 is the host" "rc=0 docker=[compose run --rm spanner-init;]" "$(run 0 0 init)"

# In the dev container: no docker at all, and success even though docker would fail.
check "container up skips docker" "rc=0 docker=[]" "$(run 1 1 up)"
check "container init skips docker" "rc=0 docker=[]" "$(run 1 1 init)"

# Anything else is a usage error, in or out of the container.
check "no argument is a usage error" "rc=2 docker=[]" "$(run "" 0)"
check "an unknown command is a usage error" "rc=2 docker=[]" "$(run 1 0 down)"

if [[ $failures -gt 0 ]]; then
  echo "infra-test: $failures failed" >&2
  exit 1
fi
echo "infra-test: all passed"
