# The API image: a static, pure-Go binary on distroless, running as the nonroot user (uid 65532)
# with no shell or package manager, so Kubernetes can enforce runAsNonRoot and
# readOnlyRootFilesystem (design 85). Distroless static ships the CA bundle, tzdata and
# /etc/passwd. Base images are pinned by digest.
#
# Build from the repo root: docker build -f api/Dockerfile -t api .
# The vercel-drain receiver (design 53, Decision 6) is a second target in the same module:
#   docker build -f api/Dockerfile --target vercel-drain -t vercel-drain .

FROM golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

WORKDIR /build

# Copy go.mod/sum for dependency caching (db and obs modules needed via replace)
COPY db/go.mod db/go.sum ./db/
COPY obs/go.mod obs/go.sum ./obs/
COPY api/go.mod api/go.sum ./api/
RUN cd api && go mod download

# Copy source and build
COPY db/ ./db/
COPY obs/ ./obs/
COPY api/ ./api/
RUN cd api && CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/server

FROM builder AS vercel-drain-builder
RUN cd api && CGO_ENABLED=0 go build -trimpath -o /out/vercel-drain ./cmd/vercel-drain

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3 AS vercel-drain

COPY --from=vercel-drain-builder /out/vercel-drain /usr/local/bin/vercel-drain

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/vercel-drain"]

# The API is the last stage, so it stays the default target.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3 AS api

COPY --from=builder /out/api /usr/local/bin/api

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
