# The dev image (docs/design/710-containerized-dev.md): the pinned toolchain that runs the repo's own
# Task targets in a container, so a contributor needs only Docker and Task. `task docker:test`,
# `docker:lint`, `docker:cover`, `docker:shell` and `docker:run` use it through the compose service
# `dev` (profile dev), which bind-mounts the checkout at /work and runs as the host user.
#
# Versions follow the repo's sources of truth: Go from the go.mod files, Node's major from
# web/.nvmrc, golangci-lint from ci.yml. Every image is pinned by digest (design #85), and Dependabot's
# docker ecosystem bumps them. Nothing is copied from the checkout, so the build context is dev/.
FROM node:24.21.0-trixie@sha256:be40f6a87b9b22215ddb20da0a2320a5c6d583fe3ee3b0024d9fa4f05b40c8fd AS node

# The official binary, built with Go 1.27.1 (golangci-lint's docs don't guarantee `go install`).
FROM golangci/golangci-lint:v2.14.0@sha256:ad862ba6b3798cbe0fd9fd7408d498fd74fbd2623a92406b2fd3898faf0bf98f AS golangci-lint

# Debian, not Alpine: glibc and gcc for `go test -race`, plus git, curl and Bash 5.
FROM golang:1.27.1-trixie@sha256:3b77fc618ec235a1ab412de7737f120dd507c57e8d87de4cbb7994fb94275ed5

COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

COPY --from=golangci-lint /usr/bin/golangci-lint /usr/local/bin/golangci-lint

# Task through the Go checksum database, as wrench is installed today.
RUN GOBIN=/usr/local/bin go install github.com/go-task/task/v3/cmd/task@v3.54.0 \
 && rm -rf /go/pkg /root/.cache

# jq for the db scripts, envsubst (gettext-base) for the runbook check (task lint:runbooks).
RUN apt-get update \
 && apt-get install -y --no-install-recommends gettext-base jq \
 && rm -rf /var/lib/apt/lists/*

# The container runs as the host uid (JAB_UID), which has no passwd entry, so the caches and HOME are
# world-writable; an empty named volume mounted here takes the directory's mode. /go/pkg itself too:
# `go run pkg@version` (wrench) writes the checksum database's state to /go/pkg/sumdb. safe.directory
# lets git read a checkout owned by another uid; --system keeps it inside the image.
RUN install -d -m 1777 /go/pkg /go/pkg/mod /go/.cache /home/dev /work/web/node_modules \
 && git config --system safe.directory '*'

# A named user for .devcontainer/devcontainer.json (#830): the Dev Containers extension changes its
# uid and gid to the host user's on Linux (updateRemoteUserUID), which it does only for a user in
# /etc/passwd. `task docker:*` doesn't use it: they run as JAB_UID.
RUN useradd --uid 1000 --user-group --no-create-home --home-dir /home/dev --shell /bin/bash dev

# GOTOOLCHAIN=local fails loudly on a Go mismatch instead of downloading another toolchain.
# TASK_TEMP_DIR keeps Task's checksums (web:install) apart from the host's .task/ in the checkout.
ENV GOMODCACHE=/go/pkg/mod \
    GOCACHE=/go/.cache/go-build \
    GOTOOLCHAIN=local \
    HOME=/home/dev \
    npm_config_cache=/home/dev/.npm \
    TASK_TEMP_DIR=/home/dev/.task \
    NEXT_TELEMETRY_DISABLED=1 \
    JAB_IN_CONTAINER=1

WORKDIR /work
