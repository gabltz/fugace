# syntax=docker/dockerfile:1
# TODO(M0): pin the base image by digest: golang:1.27-alpine@sha256:...
ARG GO_VERSION=1.27

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN apk add --no-cache ca-certificates
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

FROM scratch
# CA roots: needed to reach the ACME API over HTTPS (only used when FUGACE_HOST is set)
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/fugace /fugace
USER 65532:65532
# 8080 http (redirect + ACME), 8443 https, 6667 irc, 6697 ircs
EXPOSE 8080 8443 6667 6697
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/fugace", "healthcheck"]
ENTRYPOINT ["/fugace"]
