#!/usr/bin/env bash
# Runs the pre-commit checks of the named modules: their tests and linters, as .githooks/pre-commit
# did itself before (docs/design/710-containerized-dev.md, decision 7).
#
# usage: scripts/check-modules.sh <db|api|obs|pipeline|web>...
#
# It runs them on the host when go, golangci-lint and npm are all on PATH, and otherwise in the dev
# container through `docker compose run --rm dev`, which starts the emulators and Redis first
# (the image is built on first use, or with task docker:build). JAB_HOOKS=native or JAB_HOOKS=docker
# picks one regardless. Inside the dev container (JAB_IN_CONTAINER=1) they always run in place.
# On the host, the db tests need the Spanner emulator up (task infra:up), as before.
#
# Every check runs even after one fails; the exit status is 1 when any failed, 2 on a usage error.
# Runs on the host, so it stays Bash 3.2-safe (macOS): no arrays, which `set -u` rejects when empty.
set -euo pipefail

usage() {
  echo "usage: scripts/check-modules.sh <db|api|obs|pipeline|web>... (env JAB_HOOKS=native|docker)" >&2
  exit 2
}

[ $# -gt 0 ] || usage
for module in "$@"; do
  case $module in
    db | api | obs | pipeline | web) ;;
    *) usage ;;
  esac
done
case ${JAB_HOOKS:-} in
  "" | native | docker) ;;
  *) usage ;;
esac

cd "$(dirname "$0")/.."

# Where to run: what JAB_HOOKS says, else in the container when a tool is missing.
mode=${JAB_HOOKS:-}
why="JAB_HOOKS=docker"
if [ "${JAB_IN_CONTAINER:-}" = 1 ]; then
  mode=native
elif [ -z "$mode" ]; then
  mode=native
  for tool in go golangci-lint npm; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      mode=docker
      why="$tool isn't on PATH"
      break
    fi
  done
fi

if [ "$mode" = docker ]; then
  if ! command -v docker >/dev/null 2>&1; then
    echo "check-modules.sh: $why and docker isn't either: install Go, golangci-lint and Node, or Docker" >&2
    exit 1
  fi
  echo "==> $why: running the checks in the dev container (docker compose run --rm dev)"
  # As task docker:* does: the host user owns what the checks write, and Docker would create the
  # node_modules volume's missing mount point in the checkout as root.
  mkdir -p web/node_modules
  JAB_UID=$(id -u) JAB_GID=$(id -g) exec docker compose run --rm -T dev scripts/check-modules.sh "$@"
fi

failed=false

# check <module> <what> <command>...: runs the command in the module's directory.
check() {
  local module=$1 what=$2
  shift 2
  echo "==> $module: $what..."
  if ! (cd "$module" && "$@"); then
    failed=true
  fi
}

for module in "$@"; do
  case $module in
    web)
      # The container's node_modules is a volume of its own, empty until the first install; Task's
      # checksum makes it a no-op after that.
      if [ "${JAB_IN_CONTAINER:-}" = 1 ]; then
        echo "==> web: installing dependencies..."
        task web:install || failed=true
      fi
      check web "running lint" npm run lint
      check web "running type check" npm run typecheck
      check web "running tests" npm test
      ;;
    *)
      check "$module" "running tests" go test ./...
      check "$module" "running golangci-lint" golangci-lint run
      ;;
  esac
done

if $failed; then
  exit 1
fi
