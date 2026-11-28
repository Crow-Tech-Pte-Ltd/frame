# syntax=docker/dockerfile:1
#
# frame-server container image. Build from the REPOSITORY ROOT:
#
#   docker build -f deploy/Dockerfile -t frame-server .
#
# (deploy/docker-compose.build.yml does this for you.) Official multi-arch
# images (linux/amd64, linux/arm64) are published as
# ghcr.io/crow-tech-pte-ltd/frame-server by .github/workflows/release.yml.
# The Go stage runs on the build machine's platform and cross-compiles for
# the target platform, so multi-arch builds need no CPU emulation.
#
# Static Go binary (CGO_ENABLED=0, pure-Go SQLite) on distroless/static: no
# shell, no package manager, CA certificates + tzdata included, runs as the
# unprivileged "nonroot" user (uid/gid 65532). All state lives in /data.
#
# HEALTHCHECK: distroless has no shell, curl or wget, so this image does not
# declare one. Check https://<your-server>/healthz from outside (uptime monitor,
# Caddy's active health checks in deploy/Caddyfile) — see docs/SELF-HOSTING.md.

# Exact patch release: the golang images set GOTOOLCHAIN=local, so go.mod's
# toolchain line is ignored here and a stale cached "1.26" image would build
# with an old, vulnerable standard library. Bump the tag and digest with go.mod.
FROM --platform=$BUILDPLATFORM golang:1.26.11-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS build
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify
COPY server/ ./
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/frame-server ./cmd/frame-server \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
COPY --from=build /out/frame-server /usr/local/bin/frame-server
# /data must be owned by the runtime user so a fresh named volume is writable.
COPY --from=build --chown=65532:65532 /out/data /data
ENV FRAME_DATA_DIR=/data \
    FRAME_LISTEN=:8080
VOLUME ["/data"]
# 8080: plain HTTP (behind Caddy / Cloudflare Tunnel / another proxy).
# 80 + 443: only when FRAME_DOMAIN is set (built-in Let's Encrypt). Docker lets
# non-root processes bind ports < 1024 inside the container by default.
EXPOSE 8080 80 443
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/frame-server"]
CMD ["serve"]
