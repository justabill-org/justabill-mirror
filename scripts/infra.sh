#!/usr/bin/env bash
# Starts the local services and creates the emulator database, for task infra:up and task migrate
# (docs/design/710-containerized-dev.md).
#
# usage: scripts/infra.sh up|init
#   up    start the Spanner emulator, the Auth emulator and Redis
#   init  create the emulator instance and the database from db/schema.sql if they're missing
#         (exits with spanner-init's status)
#
# Inside the dev container (JAB_IN_CONTAINER=1, task docker:*) both return at once: compose already
# started the services and ran spanner-init as the dev service's dependencies, and the container
# has no Docker. Runs on the host, so it stays Bash 3.2-safe (macOS).
set -euo pipefail

cmd=${1:-}
case $cmd in
  up | init) ;;
  *)
    echo "usage: scripts/infra.sh up|init" >&2
    exit 2
    ;;
esac

if [ "${JAB_IN_CONTAINER:-}" = 1 ]; then
  echo "infra.sh $cmd: skipped in the dev container (compose started the services)"
  exit 0
fi

case $cmd in
  up) exec docker compose up -d spanner-emulator auth-emulator redis ;;
  init) exec docker compose run --rm spanner-init ;;
esac
