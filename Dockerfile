# syntax=docker/dockerfile:1.27.1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# Base image pinned by digest (multi-arch index); Dependabot keeps it current.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
WORKDIR /src
COPY go.mod go.sum* ./
# The image already ships ca-certificates: no unpinned package fetch.
RUN test -s /etc/ssl/certs/ca-certificates.crt
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w -buildid= -X main.version=${VERSION}" \
      -o /out/fugace ./cmd/fugace
# Empty mount point for the opt-in certificate cache (FUGACE_CERT_CACHE=/cache):
# a new volume inherits its owner and mode, and nothing is created at runtime.
RUN mkdir -p /out/cache

FROM scratch
# CA roots: needed to reach the ACME API over HTTPS (only used when FUGACE_HOST is set)
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/fugace /fugace
COPY --from=build --chown=65532:65532 --chmod=0700 /out/cache /cache
USER 65532:65532
# 8080 http (redirect + ACME), 8443 https, 6667 irc, 6697 ircs
EXPOSE 8080 8443 6667 6697
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/fugace", "healthcheck"]
ENTRYPOINT ["/fugace"]
