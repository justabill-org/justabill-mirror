# The migrate image: wrench plus db/migrations, run by the production deploy as the
# `migrate-<version>` Kubernetes Job before each rollout (design 28, "How production migrations
# run"). The entrypoint, scripts/db-migrate.sh --audit, logs the version before and after, flags
# pending DROPs, exits non-zero on a failed migration and prints the resulting schema.
#
# Build from the repo root: docker build -f db/Dockerfile -t migrate .
# Run: SPANNER_PROJECT, SPANNER_INSTANCE and SPANNER_DATABASE (and SPANNER_EMULATOR_HOST
# locally). It needs a writable /tmp (an emptyDir under a read-only root filesystem).

FROM golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

# The Go checksum database verifies wrench and its dependencies at this exact version.
RUN CGO_ENABLED=0 GOBIN=/out go install -trimpath github.com/cloudspannerecosystem/wrench@v1.13.5

# Alpine rather than distroless: the entrypoint is a bash script.
FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

RUN apk add --no-cache bash ca-certificates
COPY --from=builder /out/wrench /usr/local/bin/wrench
COPY scripts/db-migrate.sh /app/scripts/db-migrate.sh
COPY scripts/lib/migrations.sh /app/scripts/lib/migrations.sh
COPY db/migrations/ /app/db/migrations/

ENV WRENCH=/usr/local/bin/wrench
USER 65532:65532
ENTRYPOINT ["/app/scripts/db-migrate.sh", "--audit"]
