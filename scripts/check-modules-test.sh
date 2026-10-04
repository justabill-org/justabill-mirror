#!/usr/bin/env bash
# Tests scripts/check-modules.sh and the module detection in .githooks/pre-commit. go,
# golangci-lint, npm, task and docker are stand-ins that record where they ran and with what, and
# exit with $<TOOL>_EXIT; each case puts only some of them on PATH. Run: scripts/check-modules-test.sh
# Bash 3.2-safe like the scripts it tests, so it can run under macOS's Bash.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0
check() { # check <name> <expected> <actual>
  if [[ $2 == "$3" ]]; then echo "ok   $1"; else echo "FAIL $1: expected [$2], got [$3]"; failures=$((failures + 1)); fi
}

# Git exports GIT_DIR and the like to hooks; the throwaway repo below must not inherit them.
while IFS= read -r var; do unset "$var"; done < <(compgen -e | grep '^GIT_')

# A checkout with the script and the five module directories.
repo=$tmp/repo
mkdir -p "$repo/scripts" "$repo/db" "$repo/api" "$repo/obs" "$repo/pipeline" "$repo/web"
cp "$here/check-modules.sh" "$repo/scripts/"

# The stand-ins log "<dir> <tool> <args>" (and JAB_UID for docker) and exit with e.g. $GO_EXIT.
mkdir "$tmp/stubs"
for tool in go golangci-lint npm task docker; do
  var=$(echo "$tool" | tr 'a-z-' 'A-Z_')_EXIT
  cat >"$tmp/stubs/$tool" <<SH
#!/usr/bin/env bash
extra=""
[ "$tool" = docker ] && extra=" uid=\${JAB_UID:-unset}"
echo "\${PWD##*/} $tool \$*\$extra" >>"\$CALLS"
exit "\${$var:-0}"
SH
  chmod +x "$tmp/stubs/$tool"
done

# The system tools the script needs, without whatever else /usr/bin holds (a real go or docker).
mkdir "$tmp/sys"
for tool in bash dirname id mkdir; do
  ln -s "$(command -v "$tool")" "$tmp/sys/$tool"
done

# run <stand-ins on PATH, comma-separated> [VAR=value...] -- <args...>:
# prints "rc=<status> calls=[<calls, ;-separated>]".
run() {
  local tools=$1 bin
  shift
  bin=$tmp/bin-${tools//,/-}
  if [ ! -d "$bin" ]; then
    mkdir "$bin"
    for tool in ${tools//,/ }; do ln -s "$tmp/stubs/$tool" "$bin/$tool"; done
  fi
  local envs=""
  while [ $# -gt 0 ] && [ "$1" != -- ]; do envs+=" $1"; shift; done
  shift
  : >"$tmp/calls"
  # shellcheck disable=SC2086 # envs is a word list
  env -i HOME="$tmp" PATH="$bin:$tmp/sys" CALLS="$tmp/calls" $envs \
    "$repo/scripts/check-modules.sh" "$@" >"$tmp/out" 2>&1
  local rc=$?
  echo "rc=$rc calls=[$(tr '\n' ';' <"$tmp/calls")]"
}

all=go,golangci-lint,npm,task,docker
docker_obs="repo docker compose run --rm -T dev scripts/check-modules.sh obs uid=$(id -u);"

# With the toolchain on PATH: in place, as the hook did, every check of every module named.
check "toolchain: Go modules run tests and lint in their directories" \
  "rc=0 calls=[db go test ./...;db golangci-lint run;api go test ./...;api golangci-lint run;]" \
  "$(run "$all" -- db api)"
check "toolchain: web runs lint, typecheck and tests, without an install" \
  "rc=0 calls=[web npm run lint;web npm run typecheck;web npm test;]" "$(run "$all" -- web)"
check "toolchain: a failing test fails it, and the lint still runs" \
  "rc=1 calls=[pipeline go test ./...;pipeline golangci-lint run;]" "$(run "$all" GO_EXIT=1 -- pipeline)"
check "toolchain: a failing lint fails it" \
  "rc=1 calls=[web npm run lint;web npm run typecheck;web npm test;]" "$(run "$all" NPM_EXIT=1 -- web)"

# Any of the three missing: the dev container, as the host user, with the same modules.
check "no go: the dev container" "rc=0 calls=[$docker_obs]" "$(run golangci-lint,npm,docker -- obs)"
check "no golangci-lint: the dev container" "rc=0 calls=[$docker_obs]" "$(run go,npm,docker -- obs)"
check "no npm: the dev container" "rc=0 calls=[$docker_obs]" "$(run go,golangci-lint,docker -- obs)"
check "the dev container gets every module named" \
  "rc=0 calls=[repo docker compose run --rm -T dev scripts/check-modules.sh db api pipeline web uid=$(id -u);]" \
  "$(run docker -- db api pipeline web)"
check "a failing check in the container fails it" "rc=1 calls=[$docker_obs]" "$(run docker DOCKER_EXIT=1 -- obs)"
run docker -- obs >/dev/null
check "it says, in one line, why it uses the container" \
  "==> go isn't on PATH: running the checks in the dev container (docker compose run --rm dev)" \
  "$(cat "$tmp/out")"
check "neither the toolchain nor docker: it fails" "rc=1 calls=[]" "$(run npm -- obs)"

# JAB_HOOKS picks the path regardless of what's installed.
check "JAB_HOOKS=docker with the toolchain: the dev container" \
  "rc=0 calls=[$docker_obs]" "$(run "$all" JAB_HOOKS=docker -- obs)"
check "JAB_HOOKS=native without npm: in place" \
  "rc=0 calls=[obs go test ./...;obs golangci-lint run;]" "$(run go,golangci-lint,docker JAB_HOOKS=native -- obs)"

# In the dev container (where docker:* and the hook's container path run it): always in place, and
# web installs into the container's own node_modules first.
check "in the container: in place, even with JAB_HOOKS=docker" \
  "rc=0 calls=[obs go test ./...;obs golangci-lint run;]" \
  "$(run go,golangci-lint,task JAB_IN_CONTAINER=1 JAB_HOOKS=docker -- obs)"
check "in the container: web installs first" \
  "rc=0 calls=[repo task web:install;web npm run lint;web npm run typecheck;web npm test;]" \
  "$(run go,golangci-lint,npm,task JAB_IN_CONTAINER=1 -- web)"

check "no module is a usage error" "rc=2 calls=[]" "$(run "$all" --)"
check "an unknown module is a usage error" "rc=2 calls=[]" "$(run "$all" -- db docs)"
check "an unknown JAB_HOOKS is a usage error" "rc=2 calls=[]" "$(run "$all" JAB_HOOKS=auto -- db)"

# The hook: which modules its staged files name. A throwaway repo with the hook and a stand-in
# check-modules.sh that records its arguments and exits with $CHECK_EXIT.
hookrepo=$tmp/hookrepo
mkdir -p "$hookrepo/.githooks" "$hookrepo/scripts"
cp "$here/../.githooks/pre-commit" "$hookrepo/.githooks/"
cat >"$hookrepo/scripts/check-modules.sh" <<'SH'
#!/usr/bin/env bash
echo "$*" >>"$CALLS"
exit "${CHECK_EXIT:-0}"
SH
chmod +x "$hookrepo/scripts/check-modules.sh"
git -C "$hookrepo" init -q

# hook [CHECK_EXIT] <files to stage...>: prints "rc=<status> modules=[<check-modules.sh calls>]".
hook() {
  local exit=$1
  shift
  (
    cd "$hookrepo" || exit 1
    git rm -rq --cached . >/dev/null 2>&1
    for f in "$@"; do
      mkdir -p "$(dirname "$f")"
      echo x >"$f"
      git add "$f"
    done
    : >"$tmp/calls"
    CALLS="$tmp/calls" CHECK_EXIT=$exit .githooks/pre-commit >/dev/null 2>&1
    echo "rc=$? modules=[$(tr '\n' ';' <"$tmp/calls")]"
  )
}

check "hook: db also checks api and pipeline" "rc=0 modules=[db api pipeline;]" "$(hook 0 db/a.go)"
check "hook: modules in a fixed order, once each" \
  "rc=0 modules=[api obs web;]" "$(hook 0 web/a.ts api/a.go obs/a.go api/b.go)"
check "hook: .golangci.yml checks the Go modules" \
  "rc=0 modules=[db api obs pipeline;]" "$(hook 0 .golangci.yml)"
check "hook: no module staged checks nothing" "rc=0 modules=[]" "$(hook 0 README.md)"
check "hook: a failed check fails the commit" "rc=1 modules=[pipeline;]" "$(hook 1 pipeline/a.go)"
check "hook: a usage error fails the commit" "rc=1 modules=[web;]" "$(hook 2 web/a.ts)"

if [[ $failures -gt 0 ]]; then
  echo "check-modules-test: $failures failed" >&2
  exit 1
fi
echo "check-modules-test: all passed"
