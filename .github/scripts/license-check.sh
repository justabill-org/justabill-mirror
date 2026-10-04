#!/usr/bin/env bash
# Checks every Go and npm dependency's license against .github/license-policy.json.
#
#   license-check.sh      # from the repo root; CI runs it in the Licenses job (task license:check)
#
# go-licenses reports each Go module's non-test dependencies (our own module ignored), then
# license-check.mjs reads those reports and web/package-lock.json. go-licenses runs with the
# module's own toolchain: built by an older Go, it can't load go1.27 packages.
set -euo pipefail

go_licenses=github.com/google/go-licenses/v2@v2.0.1
own_module=github.com/justabill-org/justabill
modules=(db api pipeline obs)

node --test .github/scripts/license-check.test.mjs

reports=$(mktemp -d)
trap 'rm -rf "$reports"' EXIT
args=(--npm web/package-lock.json)
for m in "${modules[@]}"; do
  echo "go-licenses: $m"
  # Its stderr is a warning for every assembly file; show it only if the report fails.
  if ! (cd "$m" && GOTOOLCHAIN="$(go env GOVERSION)" go run "$go_licenses" report \
    --ignore "$own_module" ./... >"$reports/$m.csv" 2>"$reports/$m.err"); then
    cat "$reports/$m.err" >&2
    printf '::error file=%s/go.mod::go-licenses failed for %s\n' "$m" "$m"
    exit 1
  fi
  args+=(--go "$m=$reports/$m.csv")
done

node .github/scripts/license-check.mjs "${args[@]}"
