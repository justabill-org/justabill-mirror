#!/usr/bin/env bash
# Checks the telemetry conventions registry (obs/semconv/registry) with OpenTelemetry Weaver and
# regenerates obs/semconv/names.go and web/src/lib/obs/names.ts from it, or checks that they are
# current (--check). Used by `task semconv`, `task semconv:check` and CI's Web Tests job.
# Design: docs/design/53-observability.md, Decision 3.
#
# Weaver runs from its container image, pinned by digest (WEAVER_IMAGE overrides it). It fetches
# the upstream registries the manifest pins from GitHub, so it needs network access.
set -euo pipefail

usage="usage: $0 [--check]"
mode="write"
case "${1:-}" in
  "") ;;
  --check) mode=check ;;
  *) echo "$usage" >&2; exit 2 ;;
esac

image="${WEAVER_IMAGE:-otel/weaver:v0.26.1@sha256:9094862c0ab261bdbcb079bb981f9a573b3659b130a6d2ab8616eca6ba37aaec}"
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
mkdir "$out/go" "$out/ts"

# The container runs as the caller so the files it writes are theirs; HOME=/tmp gives Weaver a
# writable cache for the registries it clones. Host networking because Docker's default bridge
# has no DNS on some of our runner VMs.
weaver() {
  docker run --rm --network host -u "$(id -u):$(id -g)" -e HOME=/tmp \
    -v "$root/obs/semconv:/semconv:ro" -v "$out:/out" -w /semconv \
    "$image" registry "$@"
}

# check: Weaver's own rules (a brief on every name, a unit and instrument on every metric) plus
# policies/justabill.rego (every name we define is under justabill.*, so none copies upstream).
weaver check --quiet -r registry -p policies
weaver generate --quiet -r registry -t templates -p policies go /out/go
weaver generate --quiet -r registry -t templates -p policies ts /out/ts

targets=("obs/semconv/names.go:$out/go/names.go" "web/src/lib/obs/names.ts:$out/ts/names.ts")
if [[ "$mode" == "write" ]]; then
  for t in "${targets[@]}"; do
    cp "${t#*:}" "$root/${t%%:*}"
    echo "semconv: wrote ${t%%:*}"
  done
  exit 0
fi

stale=false
for t in "${targets[@]}"; do
  if ! diff -u "$root/${t%%:*}" "${t#*:}"; then
    stale=true
  fi
done
if [[ "$stale" == "true" ]]; then
  echo "semconv: the generated names are stale; run \`task semconv\` and commit the result" >&2
  exit 1
fi
echo "semconv: registry checks pass and the generated names are current"
