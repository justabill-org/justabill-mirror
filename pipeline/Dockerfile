# The pipeline image: static, pure-Go binaries on distroless, running as the nonroot user
# (uid 65532) with no shell or package manager, so Kubernetes can enforce runAsNonRoot and
# readOnlyRootFilesystem (design 85). Distroless static ships the CA bundle, tzdata (spotcheck
# loads America/New_York) and /etc/passwd. Base images are pinned by digest.
#
# Build from the repo root: docker build -f pipeline/Dockerfile -t pipeline .
# The default entrypoint is pipeline-serve; run the others with an absolute path, e.g.
#   docker run --rm --entrypoint /usr/local/bin/pipeline-backfill pipeline --steps bills
# One-off Jobs for the law tables (docs/runbooks/law-refs.md) run pipeline-backfill-law-refs and
# pipeline-load-uscode. pipeline-aggregates (docs/runbooks/aggregates.md) runs in the serve pod
# through kubectl exec.

FROM golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

WORKDIR /build

# Copy go.mod/sum for dependency caching (db and obs modules needed via replace)
COPY db/go.mod db/go.sum ./db/
COPY obs/go.mod obs/go.sum ./obs/
COPY pipeline/go.mod pipeline/go.sum ./pipeline/
RUN cd pipeline && go mod download

# Copy source and build the binaries
COPY db/ ./db/
COPY obs/ ./obs/
COPY pipeline/ ./pipeline/
RUN cd pipeline && CGO_ENABLED=0 go build -trimpath -o /out/pipeline-serve ./cmd/serve
RUN cd pipeline && CGO_ENABLED=0 go build -trimpath -o /out/pipeline-backfill ./cmd/backfill
RUN cd pipeline && CGO_ENABLED=0 go build -trimpath -o /out/pipeline-spotcheck ./cmd/spotcheck
RUN cd pipeline && CGO_ENABLED=0 go build -trimpath -o /out/pipeline-backfill-law-refs ./cmd/backfill-law-refs
RUN cd pipeline && CGO_ENABLED=0 go build -trimpath -o /out/pipeline-load-uscode ./cmd/load-uscode
RUN cd pipeline && CGO_ENABLED=0 go build -trimpath -o /out/pipeline-summarize-batch ./cmd/summarize-batch
RUN cd pipeline && CGO_ENABLED=0 go build -trimpath -o /out/pipeline-aggregates ./cmd/aggregates

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3

COPY --from=builder /out/pipeline-serve /usr/local/bin/pipeline-serve
COPY --from=builder /out/pipeline-backfill /usr/local/bin/pipeline-backfill
COPY --from=builder /out/pipeline-spotcheck /usr/local/bin/pipeline-spotcheck
COPY --from=builder /out/pipeline-backfill-law-refs /usr/local/bin/pipeline-backfill-law-refs
COPY --from=builder /out/pipeline-load-uscode /usr/local/bin/pipeline-load-uscode
COPY --from=builder /out/pipeline-summarize-batch /usr/local/bin/pipeline-summarize-batch
COPY --from=builder /out/pipeline-aggregates /usr/local/bin/pipeline-aggregates

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/pipeline-serve"]
